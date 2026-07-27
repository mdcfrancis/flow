package appgen

// Package appgen implements the Evolutionary Growth Pipeline's front end: it
// turns a natural-language application objective into a structured App Envelope
// (Stage 1) and performs a Genesis Pass (Stage 3) that scaffolds each declared
// subsystem into a live, compilable cell the annealing loop can then optimize.
// (Stage 2 model-generated WIT and Stage 4 model-authored test envelopes are
// layered on top of this slice.)

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mdcfrancis/flow/compiler"
	"github.com/mdcfrancis/flow/evolution"
	"github.com/mdcfrancis/flow/execution"
	"github.com/mdcfrancis/flow/inference"
	"github.com/mdcfrancis/flow/manifest"
	"github.com/mdcfrancis/flow/storage"
)

// Reasoner is the cognitive-engine boundary.
type Reasoner = evolution.Reasoner

// GlobalConstraints are the application-wide performance targets.
type GlobalConstraints struct {
	TargetP99LatencyNS uint64 `json:"target_p99_latency_ns"`
	MaxFuelPerTransit  uint64 `json:"max_fuel_allocation_per_transit"`
}

// Subsystem is one declared component of the application. Beyond what it does
// (Semantics), it declares its SHARED-STATE INTERFACE — the contract fields it
// consumes (Reads) and produces (Writes) — so a cell is built to a precise
// coordination contract instead of having to infer the wiring from prose (which
// left renderers static and put bullet logic on the wrong field).
type Subsystem struct {
	Identity  string `json:"identity"`
	Semantics string `json:"semantics"`
	// Kind is the subsystem's declared TYPE (compute|render|input|leaf|compose) — the
	// single authoritative fact that drives its entry export, genesis contract, and
	// scenario shape. Declared by the envelope/fracture author and validated against
	// the ports below (ports win on contradiction). See kind.go.
	Kind   CellKind `json:"kind,omitempty"`
	Reads  []string `json:"reads,omitempty"`  // contract field names it reads (plus "HMI input")
	Writes []string `json:"writes,omitempty"` // contract field names it writes
	// Composition, when set, makes this subsystem a COMBINATOR driver: HDM generates
	// the boilerplate that applies a leaf function cell across the arrays via
	// cell-dispatch, and the model implements only the small leaf (a separate
	// subsystem).
	Composition *Composition `json:"composition,omitempty"`
}

// Composition specifies a data-parallel component built from a combinator + a leaf
// function cell, instead of a hand-written kernel.
type Composition struct {
	Combinator string `json:"combinator"`          // "map" (the P1 combinator)
	Leaf       string `json:"leaf"`                // identity of the leaf function subsystem
	In         string `json:"in"`                  // input contract array field
	Out        string `json:"out"`                 // output contract array field (i32 per element)
	ElemWords  int    `json:"elemWords,omitempty"` // words per input element (default 1)
}

// AppEnvelope is the structured decomposition of an application objective.
type AppEnvelope struct {
	ApplicationNamespace  string            `json:"application_namespace"`
	GlobalConstraints     GlobalConstraints `json:"global_constraints"`
	SubsystemRequirements []Subsystem       `json:"subsystem_requirements"`
	// Objective is the original natural-language ask, persisted so the
	// architecture-completeness critic can judge the app against it.
	Objective string `json:"objective,omitempty"`
}

const envelopePrompt = `You decompose a natural-language application objective into a strict JSON
"target specification envelope". Output ONLY a JSON object, no prose or fences:
{
  "application_namespace": "urn:hdm:apps:<short-name>",
  "global_constraints": { "target_p99_latency_ns": <int>, "max_fuel_allocation_per_transit": <int> },
  "subsystem_requirements": [
    { "identity": "urn:hdm:apps:<short-name>:<part>",
      "kind": "compute|render|input|leaf",
      "semantics": "<what it does, in terms of the shared state it reads/writes>",
      "reads":  ["<shared field it consumes>", "HMI input"],
      "writes": ["<shared field it produces>"] }
  ]
}
Declare each subsystem's KIND — its type — and make its reads/writes MATCH the kind:
- "compute": updates shared state (physics, logic). WRITES ≥1 field, draws nothing.
- "render": a live VIEW. READS state and draws it; WRITES NO state (writes: []).
- "input": reads operator input. "HMI input" in reads, WRITES ≥1 state field.
- "leaf": a pure per-element function on the arg pointer (see COMPOSITION); reads: [], writes: [].
A cell that WRITES state is never a "render" — split "simulate AND draw" into a compute
writer and a separate render view. (Kinds are validated against reads/writes; ports win.)
The subsystems coordinate ONLY through named SHARED-STATE fields (snake_case, e.g.
player_x, bullet_x, bullet_active, invader_offset_x). Every subsystem declares its
interface: which fields it "reads" and which it "writes". Use "HMI input" in
"reads" for a cell that consumes keyboard/mouse. This is the whole point — a cell
is built to its declared interface, so name the fields consistently across
subsystems (the writer of player_x and its reader must use the SAME name).

Decompose for INCREMENTAL construction, keeping the first version tiny:
- Emit the FEWEST subsystems that deliver a working first version (prefer 2-3, max 4),
  each a distinct urn identity doing exactly ONE thing, buildable as one small cell.
- Think MVP: OMIT speculative features (scoring, levels, menus, sound, win/lose).
- Wire the data flow end-to-end: input WRITES state, logic UPDATES state, and the
  view READS state. Every field that is written must have a reader, or it is dead.
- The VIEW/renderer is a LIVE view: it READS the dynamic shared state (positions,
  flags) every frame and draws entities AT those values. Do NOT specify it as a
  static or initial-only scene — a view that ignores the state renders a frozen
  game. Give it "reads" for every field whose entity it must draw.
- For a game: one view cell that reads+draws the state, plus separate cells that
  write it (one reads HMI input and writes the player position; others update
  bullet/enemy state).

COMPOSITION (data-parallel kernels): when a subsystem computes a value FOR EACH cell
of a grid/array (e.g. a fractal's escape-time per pixel, a heatmap, a per-entity
update), do NOT hand-write the whole nested-loop kernel. Instead emit TWO subsystems:
  1. a small LEAF compute subsystem — its run-tick reads ONE element's inputs at the
     arg pointer and returns one i32 (e.g. "read (cr,ci) as two f32 at the arg
     pointer; iterate z=z*z+c up to 64; return the escape iteration count"), and
  2. a COMPOSITION subsystem that maps the leaf across the array, with:
     "composition": { "combinator": "map", "leaf": "<leaf identity>",
                      "in": "<input array field>", "out": "<output array field>",
                      "elemWords": <words per input element> }
HDM GENERATES the map driver; you implement only the leaf. Declare the input/output
as ARRAY contract fields ("i32[N]"). Prefer this for any per-element grid computation
— it is far more reliable than a monolithic kernel.

WORKED EXAMPLE (a Mandelbrot viewer) — note the "composition" object is REQUIRED for
the map subsystem; do not hand-write the grid loop:
{ "application_namespace": "urn:hdm:apps:mandelbrot",
  "subsystem_requirements": [
    { "identity": "urn:hdm:apps:mandelbrot:escape", "kind": "leaf",
      "semantics": "LEAF function: read two f32 (cr, ci) at the arg pointer; iterate z=z*z+c from 0 up to 64; return the escape iteration count",
      "reads": [], "writes": [] },
    { "identity": "urn:hdm:apps:mandelbrot:grid", "kind": "compute",
      "semantics": "map the escape leaf across the grid, writing escape_times",
      "reads": ["grid_coords"], "writes": ["escape_times"],
      "composition": { "combinator": "map", "leaf": "urn:hdm:apps:mandelbrot:escape",
                       "in": "grid_coords", "out": "escape_times", "elemWords": 2 } },
    { "identity": "urn:hdm:apps:mandelbrot:renderer", "kind": "render",
      "semantics": "read escape_times and draw a colored cell per value",
      "reads": ["escape_times"], "writes": [] }
  ] }
The LEAF has empty reads/writes (it works on the arg pointer, not shared fields); the
COMPOSITION subsystem carries the "composition" object.`

// scaffoldComposition builds a combinator DRIVER subsystem: it resolves the in/out
// contract arrays, GENERATES the driver WAT (execution.GenerateMapDriver) that
// applies the leaf across them, compiles it, and registers it as the subsystem cell.
// The driver is deterministic boilerplate — no acceptance checks; the LEAF (a
// separate subsystem) carries the per-element behavior the model must get right.
func (g *Grower) scaffoldComposition(ctx context.Context, env *AppEnvelope, sub Subsystem) error {
	c := sub.Composition
	g.phase("growing", "generating composition driver "+sub.Identity, sub.Identity)
	contract := evolution.LoadContract(g.ledger, env.ApplicationNamespace)
	if contract == nil {
		return fmt.Errorf("composition %s: no contract", sub.Identity)
	}
	inOff, inWords, ok1 := contractField(contract, c.In)
	outOff, outWords, ok2 := contractField(contract, c.Out)
	if !ok1 || !ok2 {
		return fmt.Errorf("composition %s: fields %q/%q not in contract", sub.Identity, c.In, c.Out)
	}
	ew := c.ElemWords
	if ew < 1 {
		ew = 1
	}
	// n = output element count (one i32 per element); guard input covers n*elemWords.
	n := outWords
	if n < 1 {
		n = 1
	}
	if inWords < n*ew {
		n = inWords / ew // input array is the tighter bound
	}
	var driver string
	switch c.Combinator {
	case "", "map":
		driver = execution.GenerateMapDriver(c.Leaf, inOff, outOff, n, ew)
	default:
		return fmt.Errorf("composition %s: unsupported combinator %q", sub.Identity, c.Combinator)
	}
	art, err := g.sieve.CompileGenotype(driver)
	if err != nil || !art.SyntaxPassed {
		return fmt.Errorf("composition %s: driver did not compile: %v (%s)", sub.Identity, err, art.ErrorContext)
	}
	sem := manifest.SemanticManifest{
		FunctionalIntent: fmt.Sprintf("%s(%s) over %s -> %s (generated combinator driver)", c.Combinator, c.Leaf, c.In, c.Out),
		DomainTags:       []string{nsTag(env.ApplicationNamespace), "composition"},
	}
	h, _, err := g.repo.PutCell(sub.Identity, driver, art.Bytecode, sem, 0)
	if err != nil {
		return fmt.Errorf("seed composition %s: %w", sub.Identity, err)
	}
	if err := g.repo.SeedRef(sub.Identity, h); err != nil {
		return fmt.Errorf("point composition %s: %w", sub.Identity, err)
	}
	g.event("create", sub.Identity, fmt.Sprintf("generated %s driver (leaf %s, %d elements)", c.Combinator, c.Leaf, n))
	return nil
}

// contractField returns a contract field's offset and word width (typeWords) by
// name (case-insensitive), or ok=false.
func contractField(c *evolution.AppContract, name string) (offset, words int, ok bool) {
	for _, f := range c.Fields {
		if strings.EqualFold(f.Name, name) {
			return f.Offset, typeWords(f.Type), true
		}
	}
	return 0, 0, false
}

// isCompositionLeaf reports whether a subsystem is the leaf function of some
// composition in the envelope (so it must be tested as a pure per-element function).
func isCompositionLeaf(env *AppEnvelope, id string) bool {
	for _, s := range env.SubsystemRequirements {
		if s.Composition != nil && s.Composition.Leaf == id {
			return true
		}
	}
	return false
}

const leafScenarioPrompt = `Author acceptance SCENARIOS for a per-element FUNCTION cell used by a map
combinator. Its run-tick reads its input element from shared memory at the ARG
POINTER, which the harness places at offset 0x10000, and RETURNS one i32.
Author 4-8 scenarios: each SEEDS the input words at 0x10000 and asserts the exact
returned value. Output ONLY JSON, no prose or fences:
{"scenarios":[{"name":"<id>","steps":1,"seed":[{"at":"0x10000","u32":[<w0>,<w1>...]}],"expect":{"result":<int32>}}]}
The seed words are the RAW little-endian representation the cell reads (e.g. two f32
bit patterns for a complex point c=(cr,ci): cr at 0x10000, ci at 0x10004). Choose
inputs whose correct result you can state EXACTLY (e.g. c far outside the set escapes
in 1-2 iterations; c=0 never escapes and returns the maximum iteration count).`

// leafScenarios authors ABI-conforming acceptance scenarios for a composition leaf:
// seed the input element at the arg pointer (0x10000), assert the returned i32.
func (g *Grower) leafScenarios(ctx context.Context, sub Subsystem) *evolution.AcceptanceSuite {
	resp, err := g.model.InvokeReasoning(ctx, g.prompt("leaf-scenario", leafScenarioPrompt), sub.Semantics)
	if err != nil {
		return nil
	}
	js := extractJSON(resp)
	if js == "" {
		return nil
	}
	var s evolution.AcceptanceSuite
	if json.Unmarshal([]byte(js), &s) != nil {
		return nil
	}
	return &s
}

// fallbackSkeleton is a minimal valid genesis cell used when the model cannot
// produce a compilable skeleton — the cell exists (crude, high-energy) so the
// annealing loop can improve it later.
const fallbackSkeleton = `(module
  (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 100))
  (func (export "run-tick") (param i32 i32) (result i32) i32.const 0))`

// Grower runs the growth pipeline against a ledger and cognitive engine.
type Grower struct {
	ledger    *storage.LedgerEngine
	repo      *manifest.Repository
	sieve     *compiler.CompilerService
	model     Reasoner
	SieveIter int
	// FluxEnabled mirrors the orchestrator's macro-WAT path: when on, a freshly
	// scaffolded cell is seeded with a no-op macro-WAT program (not a raw-WAT
	// skeleton), so the stored genome is macro-WAT from birth and the synthesis loop
	// iterates on that draft ("improve THIS") instead of building from scratch atop WAT.
	FluxEnabled bool
	// Activity, when set, receives growth progress so the console can show the
	// app being scaffolded subsystem by subsystem. Optional; nil-safe.
	Activity evolution.ActivitySink
}

// NewGrower constructs a Grower.
// modelFor selects the client bound to a logical model type when the model is a
// router, else the single model. Faculties that want a specific type (the sieve →
// code, cheap gates → fast) call this; everything else uses g.model, which the
// router resolves to the reason type by default.
func (g *Grower) modelFor(mt inference.ModelType) Reasoner {
	if r, ok := g.model.(*inference.ModelRouter); ok {
		return r.For(string(mt))
	}
	return g.model
}

func NewGrower(ledger *storage.LedgerEngine, model Reasoner) *Grower {
	return &Grower{
		ledger:    ledger,
		repo:      manifest.NewRepository(ledger),
		sieve:     compiler.NewCompilerService(),
		model:     model,
		SieveIter: 5,
	}
}

// phase / event are nil-safe activity shims.
func (g *Grower) phase(name, reason, target string) {
	if g.Activity != nil {
		g.Activity.Phase(name, reason, target)
	}
}

func (g *Grower) event(kind, cell, detail string) {
	if g.Activity != nil {
		g.Activity.Event(kind, cell, detail)
	}
}

// CompileEnvelope compiles an NL objective into an App Envelope and persists it
// under "<namespace>:envelope".
func (g *Grower) CompileEnvelope(ctx context.Context, objective string) (*AppEnvelope, error) {
	resp, err := g.model.InvokeReasoning(ctx, g.prompt("envelope", envelopePrompt), objective)
	if err != nil {
		return nil, fmt.Errorf("envelope reasoning failed: %w", err)
	}
	js := extractJSON(resp)
	if js == "" {
		evolution.AddPromptGrievance(g.ledger, "envelope", "output contained no JSON object for the target-specification envelope")
		return nil, fmt.Errorf("no JSON envelope in model output")
	}
	var env AppEnvelope
	if err := json.Unmarshal([]byte(js), &env); err != nil {
		evolution.AddPromptGrievance(g.ledger, "envelope", "output was not valid JSON matching the {application_namespace, global_constraints, subsystem_requirements[]} schema")
		return nil, fmt.Errorf("decode envelope: %w", err)
	}
	if env.ApplicationNamespace == "" || len(env.SubsystemRequirements) == 0 {
		return nil, fmt.Errorf("envelope missing namespace or subsystems")
	}
	// Canonicalize the namespace so the SAME app maps to a stable identity across
	// grows — the model otherwise drifts between separators ("space-invaders" vs
	// "space_invaders"), which spawns duplicate apps when a prompt is re-built.
	env.ApplicationNamespace = canonicalNamespace(env.ApplicationNamespace)
	for i := range env.SubsystemRequirements {
		env.SubsystemRequirements[i].Identity = rebaseIdentity(env.SubsystemRequirements[i].Identity, env.ApplicationNamespace)
	}
	// If this app already exists, a re-build is a REFINE, not a new app: update the
	// objective anchor and keep the existing (evolved) subsystems, so the loop
	// adapts the same app instead of duplicating it.
	if existing := LoadEnvelope(g.ledger, env.ApplicationNamespace); existing != nil {
		old := existing.Objective
		existing.Objective = objective
		saveEnvelope(g.ledger, existing)
		// A changed objective reshapes what each cell must read/write — evolve the
		// boundaries against the new goal (the cells' code then re-evolves to fit).
		if n := g.reboundOnObjectiveChange(ctx, existing.ApplicationNamespace, old, objective); n > 0 {
			g.event("boundary", existing.ApplicationNamespace, fmt.Sprintf("objective changed — re-evolved %d boundary(ies)", n))
		}
		return existing, nil
	}
	env.Objective = objective // anchor for the architecture-completeness critic
	normalizeKinds(&env)      // declared kinds validated against ports (ports win); logs overrides
	saveEnvelope(g.ledger, &env)
	return &env, nil
}

// canonicalNamespace normalizes an "urn:hdm:apps:<slug>" namespace: the prefix is
// preserved and the slug is lowercased with runs of non-alphanumerics collapsed
// to a single underscore, so "space-invaders" and "Space Invaders" both become
// "space_invaders". A namespace without the apps prefix is returned unchanged.
func canonicalNamespace(ns string) string {
	const prefix = "urn:hdm:apps:"
	if !strings.HasPrefix(ns, prefix) {
		return ns
	}
	var b strings.Builder
	lastUnd := false
	for _, r := range strings.ToLower(ns[len(prefix):]) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastUnd = false
		} else if !lastUnd {
			b.WriteByte('_')
			lastUnd = true
		}
	}
	slug := strings.Trim(b.String(), "_")
	if slug == "" {
		return ns
	}
	return prefix + slug
}

// rebaseIdentity re-parents a subsystem identity onto namespace using its last
// path segment (e.g. "urn:hdm:apps:space-invaders:renderer" → "<ns>:renderer").
func rebaseIdentity(id, namespace string) string {
	part := id
	if i := strings.LastIndex(id, ":"); i >= 0 {
		part = id[i+1:]
	}
	if part == "" {
		part = "cell"
	}
	return namespace + ":" + part
}

// AppExists reports whether an application envelope is stored for namespace.
func (g *Grower) AppExists(namespace string) bool { return LoadEnvelope(g.ledger, namespace) != nil }

// Refine updates an existing application's objective — the anchor the
// architecture-completeness critic and the curriculum build toward — WITHOUT
// re-decomposing, so a follow-up prompt refines the SAME app (the loop adds any
// now-missing subsystems and harder checks) rather than spawning a duplicate.
// Re-ensures the shared-state contract. Errors if the app does not exist.
func (g *Grower) Refine(ctx context.Context, namespace, objective string) (*AppEnvelope, error) {
	env := LoadEnvelope(g.ledger, namespace)
	if env == nil {
		return nil, fmt.Errorf("no application %s to refine", namespace)
	}
	old := env.Objective
	env.Objective = objective
	saveEnvelope(g.ledger, env)
	_, _ = g.EnsureContract(ctx, namespace)
	// A changed objective can make existing boundaries wrong — re-evolve them so the
	// enforced ports track the new goal before the cells re-evolve their code.
	if n := g.reboundOnObjectiveChange(ctx, namespace, old, objective); n > 0 {
		g.event("boundary", namespace, fmt.Sprintf("objective changed — re-evolved %d boundary(ies)", n))
	}
	return env, nil
}

// RetireApp removes an application's cells and metadata (envelope, contract,
// acceptance, friction, …) from the ledger so it no longer rehydrates — used to
// delete a stray/duplicate app. Returns how many refs were dropped. Content
// blocks are reclaimed by the GC sweep once unreachable.
func (g *Grower) RetireApp(namespace string) (int, error) {
	refs, err := g.ledger.Refs()
	if err != nil {
		return 0, err
	}
	var drop []string
	for urn := range refs {
		if urn == namespace || strings.HasPrefix(urn, namespace+":") {
			drop = append(drop, urn)
		}
	}
	if len(drop) == 0 {
		return 0, nil
	}
	return len(drop), g.ledger.DeleteRefs(drop...)
}

func envelopeRefURN(namespace string) string { return namespace + ":envelope" }

// saveEnvelope persists an envelope under "<namespace>:envelope".
func saveEnvelope(ledger *storage.LedgerEngine, env *AppEnvelope) {
	if raw, err := json.Marshal(env); err == nil {
		if h, wErr := ledger.WriteBlock(raw); wErr == nil {
			_ = ledger.UpdateRef(envelopeRefURN(env.ApplicationNamespace), h)
		}
	}
}

// LoadEnvelope returns the persisted envelope for an application namespace, or
// nil if none is stored.
func LoadEnvelope(ledger *storage.LedgerEngine, namespace string) *AppEnvelope {
	h, err := ledger.GetRef(envelopeRefURN(namespace))
	if err != nil {
		return nil
	}
	raw, err := ledger.ReadBlock(h)
	if err != nil {
		return nil
	}
	var env AppEnvelope
	if json.Unmarshal(raw, &env) != nil {
		return nil
	}
	// Defensive: derive kinds for any subsystem that lacks a (valid) one — so
	// LEGACY envelopes persisted before the ontology still get correctly-typed
	// cells, with no migration write and no change to the stored blob.
	for i := range env.SubsystemRequirements {
		if !CellKind(env.SubsystemRequirements[i].Kind).valid() {
			normalizeKinds(&env)
			break
		}
	}
	return &env
}

// Grow compiles the envelope and scaffolds every subsystem that is not already
// a live cell. It returns the envelope, the URNs of the cells it created (to
// enroll in the annealing set), and the URN of the app's UI cell if one was
// scaffolded ("" otherwise) so a viewer can auto-focus it.
func (g *Grower) Grow(ctx context.Context, objective string) (*AppEnvelope, []string, string, error) {
	g.phase("growing", "compiling application envelope for: "+objective, "")
	env, err := g.CompileEnvelope(ctx, objective)
	if err != nil {
		return nil, nil, "", err
	}
	created, uiURN, err := g.growAll(ctx, env, nil)
	g.phase("idle", fmt.Sprintf("grew %s (%d subsystems)", env.ApplicationNamespace, len(created)), "")
	return env, created, uiURN, err
}

// GrowConcurrent scaffolds every subsystem of a natural-language objective and
// enrolls each as it is created, then returns — leaving the background
// evolutionary loop to build them ALL up alongside each other (parallel
// co-evolution), rather than driving one to completion before the next. enroll
// is called per created subsystem URN (nil-safe).
func (g *Grower) GrowConcurrent(ctx context.Context, objective string, enroll func(urn string)) (*AppEnvelope, []string, string, error) {
	g.phase("growing", "compiling application envelope for: "+objective, "")
	env, err := g.CompileEnvelope(ctx, objective)
	if err != nil {
		return nil, nil, "", err
	}
	// Author the shared-state contract up front so the subsystems co-evolve
	// against an agreed memory layout (how they will coordinate).
	if _, cerr := g.EnsureContract(ctx, env.ApplicationNamespace); cerr != nil {
		g.event("hold", env.ApplicationNamespace, "contract authoring deferred: "+cerr.Error())
	}
	created, uiURN, err := g.growAll(ctx, env, enroll)
	g.phase("idle", fmt.Sprintf("scaffolded %s (%d subsystems co-evolving)", env.ApplicationNamespace, len(created)), "")
	return env, created, uiURN, err
}

// ScaffoldAll scaffolds every subsystem of a pre-compiled envelope, enrolling
// each as it is created. Split from GrowConcurrent so a caller (the /build
// console) can respond with the plan immediately and scaffold in the background.
func (g *Grower) ScaffoldAll(ctx context.Context, env *AppEnvelope, enroll func(urn string)) ([]string, string, error) {
	created, uiURN, err := g.growAll(ctx, env, enroll)
	g.phase("idle", fmt.Sprintf("scaffolded %s (%d subsystems co-evolving)", env.ApplicationNamespace, len(created)), "")
	return created, uiURN, err
}

// growAll scaffolds each not-yet-live subsystem of a compiled envelope,
// enrolling each as it comes online. Returns the created URNs and the first UI
// subsystem's URN.
func (g *Grower) growAll(ctx context.Context, env *AppEnvelope, enroll func(urn string)) (created []string, uiURN string, err error) {
	// Ensure the shared-state contract exists BEFORE scaffolding, so a compute
	// cell is authored with contract-aware coordination scenarios rather than
	// contrived scalar tests (see scaffold). Idempotent — a no-op if already
	// authored (e.g. by GrowConcurrent).
	if _, cerr := g.EnsureContract(ctx, env.ApplicationNamespace); cerr != nil {
		g.event("hold", env.ApplicationNamespace, "contract authoring deferred: "+cerr.Error())
	}
	// Seed the map with the PLANNED architecture before anything is built, so even
	// the first cell is synthesized knowing what the application is and which
	// siblings are coming.
	if err := g.RefreshAppMap(ctx, env.ApplicationNamespace); err != nil {
		g.event("hold", env.ApplicationNamespace, "app map refresh deferred: "+err.Error())
	}
	// Author the DESIGN PLAN up front, so the first synthesis implements a plan
	// rather than reinventing one. Best-effort — a fault falls back to the map.
	if err := g.AuthorPlan(ctx, env.ApplicationNamespace); err != nil {
		g.event("hold", env.ApplicationNamespace, "plan authoring deferred: "+err.Error())
	}
	// PROACTIVE DIFFICULTY DESCENT: before building anything, judge each subsystem and
	// DECOMPOSE the ones too hard to synthesize whole (e.g. multi-body physics) into simpler
	// sub-cells — a correct-first tree the optimizer later makes efficient. This turns the
	// stall->fracture ladder from a reactive last resort into a preemptive plan; a fracture's
	// children get judged in turn. Only on the live (enrolling) grow path; the reactive ladder
	// remains the backstop for anything the judgment under-shoots.
	if enroll != nil {
		if kids := g.proactiveDecompose(ctx, env.ApplicationNamespace, enroll); len(kids) > 0 {
			created = append(created, kids...)
			if fresh := LoadEnvelope(g.ledger, env.ApplicationNamespace); fresh != nil {
				*env = *fresh // pick up the decomposed subsystem list
			}
		}
	}
	for _, sub := range env.SubsystemRequirements {
		if _, e := g.ledger.GetRef(sub.Identity); e == nil {
			continue // already live
		}
		if uiURN == "" && kindOf(sub) == KindRender {
			uiURN = sub.Identity
		}
		if err = g.scaffold(ctx, env, sub); err != nil {
			return created, uiURN, err
		}
		created = append(created, sub.Identity)
		if enroll != nil {
			enroll(sub.Identity)
		}
		// Fold each component into the map AS IT LANDS, not once growth finishes:
		// the cell scaffolded next (and the evolution frames that start on this one
		// immediately) must already see the system taking shape around them.
		if err := g.RefreshAppMap(ctx, env.ApplicationNamespace); err != nil {
			g.event("hold", env.ApplicationNamespace, "app map refresh deferred: "+err.Error())
		}
	}
	return created, uiURN, nil
}

// scaffold brings one subsystem to life: draft its WIT contract, run the Genesis
// Pass into a compiled phenotype (or fallback skeleton), point its URN at a fresh
// descriptor, and author its acceptance checks (scalar tests for compute cells,
// behavioral draw-stream scenarios for UI cells). Idempotent per URN via the
// caller's already-live guard.
func (g *Grower) scaffold(ctx context.Context, env *AppEnvelope, sub Subsystem) error {
	// A composition subsystem is HDM-generated boilerplate (a combinator driver), not
	// model-synthesized — its leaf is a separate subsystem the model implements.
	if sub.Composition != nil {
		return g.scaffoldComposition(ctx, env, sub)
	}
	g.phase("growing", "synthesizing subsystem "+sub.Identity, sub.Identity)
	// Stage 2: draft and persist the WIT interface contract.
	wit := g.wit(ctx, env, sub)
	if h, err := g.ledger.WriteBlock([]byte(wit)); err == nil {
		_ = g.ledger.UpdateRef(sub.Identity+":wit", h)
	}
	// Stage 3: Genesis Pass — synthesize a skeleton conforming to the WIT.
	wat, bc := g.genesis(ctx, env, sub, wit)
	sem := manifest.SemanticManifest{
		FunctionalIntent: sub.Semantics,
		DomainTags:       []string{nsTag(env.ApplicationNamespace), "genesis"},
	}
	h, _, err := g.repo.PutCell(sub.Identity, wat, bc, sem, 0)
	if err != nil {
		return fmt.Errorf("seed subsystem %s: %w", sub.Identity, err)
	}
	if err := g.repo.SeedRef(sub.Identity, h); err != nil {
		return fmt.Errorf("point subsystem %s: %w", sub.Identity, err)
	}
	// Stage 4: autonomous QA — author acceptance checks that drive the annealing
	// loop toward target behavior:
	//   - UI cells            → behavioral draw-stream scenarios.
	//   - compute cells in an app WITH a shared-state contract → contract-aware
	//     COORDINATION scenarios (reads-based), because such a cell coordinates
	//     through the contract (read player_input, write player_x); scalar
	//     int-in/int-out tests would force a contrived integer encoding that is
	//     hard for any model to satisfy — the plateau we saw on physics-engine.
	//   - compute cells with no contract → scalar int-in/int-out tests.
	contract := evolution.LoadContract(g.ledger, env.ApplicationNamespace)
	var suite *evolution.AcceptanceSuite
	// The cell's declared KIND (validated against ports) picks the scenario shape —
	// one authoritative fact, so genesis, grading, and authoring can never disagree.
	kind := kindOf(sub)
	switch {
	case kind == KindLeaf || isCompositionLeaf(env, sub.Identity):
		// A LEAF is a pure per-element function: the harness invokes it with its
		// input element in memory at the arg pointer (DefaultPayloadOffset). Its
		// scenarios must seed THERE and assert the return value — not seed contract
		// fields (it reads none) nor a scalar param.
		suite = g.leafScenarios(ctx, sub)
	case kind == KindRender:
		suite = g.uiScenarios(ctx, sub)
		// A UI cell that visualizes shared state needs POSITION-coordination
		// scenarios (seed player_x → expect a sprite drawn there), or it renders a
		// static scene and nothing moves. Author them from the contract and append
		// to the draw-floor scenarios.
		if contract != nil {
			if coord := g.authorCoordination(ctx, sub.Semantics, contract, env.ApplicationNamespace, true, sub.Writes); coord != nil && len(coord.Scenarios) > 0 {
				if suite == nil {
					suite = &evolution.AcceptanceSuite{}
				}
				suite.Scenarios = append(suite.Scenarios, coord.Scenarios...)
			}
		}
	default:
		if contract != nil {
			suite = g.authorCoordination(ctx, sub.Semantics, contract, env.ApplicationNamespace, false, sub.Writes)
		}
		if suiteCount(suite) == 0 { // no contract, or authoring produced nothing
			suite = g.acceptance(ctx, sub)
		}
	}
	if suite != nil && (len(suite.Tests) > 0 || len(suite.Scenarios) > 0) {
		_ = evolution.SaveAcceptance(g.ledger, sub.Identity, suite)
	}
	g.event("create", sub.Identity, sub.Semantics)
	return nil
}

// StageRunner drives a single scaffolded cell toward its acceptance suite. It is
// the boundary between growth (which creates cells + checks) and evolution
// (which mutates a cell frame by frame); evolution.Orchestrator satisfies it.
type StageRunner interface {
	// ScoreCell reports how many of a cell's acceptance checks the live cell
	// currently passes (total 0 => the cell has no checks).
	ScoreCell(ctx context.Context, cellURN string) (passed, total int, err error)
	// RunFrame runs one mutation frame against the cell, committing internally
	// if the candidate improves. It reports only transport errors.
	RunFrame(ctx context.Context, cellURN string) error
}

// StageOutcome is the result of driving one subsystem stage.
type StageOutcome struct {
	URN       string
	Semantics string
	Passed    int
	Total     int
	Attempts  int
	Done      bool // all checks pass, or the stage had none to satisfy
}

// StagedResult is the outcome of a staged growth run.
type StagedResult struct {
	Envelope *AppEnvelope
	Stages   []StageOutcome
	UIURN    string
}

// GrowStaged is the staged growth pipeline: it enforces build-up one step at a
// time. It decomposes the objective, then for each subsystem IN ORDER it
// scaffolds the cell and drives acceptance frames until the cell passes all its
// checks (or the per-stage frame budget is spent) BEFORE advancing to the next.
// A stage never blocks growth forever: an exhausted budget (or an offline
// cognitive engine) advances with the best cell achieved so far.
//
// enroll, if non-nil, is called with each subsystem URN as its cell is
// scaffolded, so the caller can register it for live execution / listing before
// the (long) driving loop runs.
func (g *Grower) GrowStaged(ctx context.Context, objective string, runner StageRunner, budget int, enroll func(urn string)) (*StagedResult, error) {
	g.phase("growing", "compiling application envelope for: "+objective, "")
	env, err := g.CompileEnvelope(ctx, objective)
	if err != nil {
		return nil, err
	}
	return g.RunStages(ctx, env, runner, budget, enroll)
}

// RunStages drives a pre-compiled envelope through the staged pipeline. Split
// from GrowStaged so a caller (e.g. the /build console) can respond with the
// plan immediately and run the stages in the background.
func (g *Grower) RunStages(ctx context.Context, env *AppEnvelope, runner StageRunner, budget int, enroll func(urn string)) (*StagedResult, error) {
	if budget < 1 {
		budget = 1
	}
	res := &StagedResult{Envelope: env}
	for i, sub := range env.SubsystemRequirements {
		if res.UIURN == "" && kindOf(sub) == KindRender {
			res.UIURN = sub.Identity
		}
		out := StageOutcome{URN: sub.Identity, Semantics: sub.Semantics}
		// Skip scaffolding an already-live cell, but still drive it.
		if _, err := g.ledger.GetRef(sub.Identity); err != nil {
			if serr := g.scaffold(ctx, env, sub); serr != nil {
				return res, serr
			}
			if enroll != nil {
				enroll(sub.Identity)
			}
		}
		// Drive this stage to completion before advancing to the next.
		for out.Attempts < budget {
			if ctx.Err() != nil {
				return res, ctx.Err()
			}
			passed, total, serr := runner.ScoreCell(ctx, sub.Identity)
			out.Passed, out.Total = passed, total
			if serr == nil && (total == 0 || passed >= total) {
				out.Done = true
				break
			}
			g.phase("staging", fmt.Sprintf("stage %d/%d %s: %d/%d checks, frame %d/%d",
				i+1, len(env.SubsystemRequirements), sub.Identity, passed, total, out.Attempts+1, budget), sub.Identity)
			out.Attempts++
			if ferr := runner.RunFrame(ctx, sub.Identity); ferr != nil {
				// Transport failure (e.g. cognitive engine offline): stop
				// spending the budget and advance with what we have.
				g.event("hold", sub.Identity, "stage driver: "+ferr.Error())
				break
			}
		}
		// Final standing after driving.
		if passed, total, serr := runner.ScoreCell(ctx, sub.Identity); serr == nil {
			out.Passed, out.Total = passed, total
			out.Done = total == 0 || passed >= total
		}
		if out.Done {
			g.event("commit", sub.Identity, fmt.Sprintf("stage passed %d/%d", out.Passed, out.Total))
		} else {
			g.event("reject", sub.Identity, fmt.Sprintf("stage advanced unfinished %d/%d after %d frames", out.Passed, out.Total, out.Attempts))
		}
		res.Stages = append(res.Stages, out)
	}
	done := 0
	for _, s := range res.Stages {
		if s.Done {
			done++
		}
	}
	g.phase("idle", fmt.Sprintf("staged %s: %d/%d stages complete", env.ApplicationNamespace, done, len(res.Stages)), "")
	return res, nil
}

const witPrompt = `Draft a minimal, valid WebAssembly Interface Type (WIT) contract for a
subsystem. Output ONLY the WIT text (a package declaration, one interface, and at
least one func). No prose or code fences.`

// wit drafts and returns a WIT contract for a subsystem, falling back to a
// generic contract if the model output is not plausibly WIT.
func (g *Grower) wit(ctx context.Context, env *AppEnvelope, sub Subsystem) string {
	resp, err := g.model.InvokeReasoning(ctx, g.prompt("wit", witPrompt),
		fmt.Sprintf("namespace %s, subsystem %s: %s", env.ApplicationNamespace, sub.Identity, sub.Semantics))
	if err == nil {
		w := extractWIT(resp)
		if strings.Contains(w, "interface") && strings.Contains(w, "func") {
			return w
		}
	}
	return fmt.Sprintf("package %s;\n\ninterface subsystem {\n    /// %s\n    run-tick: func(ptr: u32, len: u32) -> s32;\n}\n",
		witPackage(env.ApplicationNamespace), sub.Semantics)
}

const acceptancePrompt = `Author a JSON suite of 6 to 12 acceptance test cases for a subsystem,
derived from its semantics. Output ONLY a JSON object, no prose or fences:
{"tests":[{"name":"<id>","input":<int32>,"expected":<int32>}, ...]}
Each case maps an integer input to the integer result a CORRECT implementation
must return.`

// acceptance authors a spec acceptance suite for a subsystem (empty if the
// model produces nothing usable — then the cell simply anneals for efficiency).
func (g *Grower) acceptance(ctx context.Context, sub Subsystem) *evolution.AcceptanceSuite {
	resp, err := g.model.InvokeReasoning(ctx, g.prompt("acceptance", acceptancePrompt), sub.Semantics)
	if err != nil {
		return nil
	}
	js := extractJSON(resp)
	if js == "" {
		return nil
	}
	var suite evolution.AcceptanceSuite
	if json.Unmarshal([]byte(js), &suite) != nil {
		return nil
	}
	return &suite
}

const scenarioPrompt = `Author behavioral acceptance SCENARIOS for a cell. A scenario SEEDS shared
memory (to mock keyboard/mouse via the HMI input register, or to set shared
game state), runs the cell's entry one or more times, and asserts on the OUTPUT:
the returned value, the emitted draw stream (render-frame cells), and/or
shared-memory contents AFTER the run (for coordination between cells). Output
ONLY JSON, no prose:
{"scenarios":[
  {"name":"<id>","seed":[{"at":"0x...","u32":[<le words>]}],
   "steps":<int>,"entry":"run-tick|render-frame",
   "expect":{
     "result":<i32?>,
     "draw":{"layer":<int?>,"op":"rect|line|circle|any","minRecords":<int?>,"moved":"left|right|up|down","nearX":<int?>,"nearY":<int?>},
     "reads":[{"field":"<contract field name>","u32":[<words>],"cmp":"<op>"}]
   }}
]}
A coordination "reads" MUST target a shared-state contract field by NAME via
"field" (e.g. "field":"player_x") — do NOT invent a raw offset; the field name is
resolved to the contract's real address, and a read on any other location is
discarded. (Seeds may use a raw "at" for the HMI input register offsets below.)
Set "entry" to the cell's export (run-tick for compute, render-frame for UI).
HMI input register (seed these u32 offsets to mock input):
  0x50000 mouseX  0x50004 mouseY  0x50008 buttons(bit0 left)  0x5000C modifiers
  0x50010 eventSeq(nonzero=new)  0x50014 eventType(2 down,3 up,4 click,5 keydown,6 keyup)
  0x50018 eventX  0x5001C eventY  0x50020 keyCode
Draw records carry a layer in the op high byte (0 basemap, 1 widget/app, 2 overlay).
COORDINATION: to test that a cell reads/writes SHARED STATE (see the shared-state
contract if given), use "reads" to assert a shared field AFTER the run. Each read
has an optional "cmp" saying HOW to compare:
  - omit / "eq": field equals the exact u32 words.
  - "increased"/"decreased"/"changed"/"unchanged": field vs its value BEFORE the
    run (no u32 needed). USE THESE for directional behavior whose exact magnitude
    is unspecified — e.g. "after a right-key event, player_x increased" (NOT
    player_x==105, since the step size is arbitrary and would reject a correct
    cell that moves by a different amount).
  - "gt"/"lt"/"ge"/"le"/"ne": compare the field to u32[0].
Assert INTENT, not magic numbers: prefer "increased"/"changed" for movement and
"eq" only for genuinely exact facts (e.g. a flag set to 1, a bullet spawned at
the player's exact x).
RENDER-FRAME cells that VISUALIZE shared state must be tested with POSITION:
seed a contract field to a value and assert a primitive is drawn there via
"nearX"/"nearY" — e.g. seed player_x=250, expect a rect nearX=250; seed player_x=40,
expect a rect nearX=40. This forces the renderer to READ the shared field and
draw the sprite at it, so the sprite actually MOVES with the state instead of
being drawn at a fixed spot. Author 2-4 scenarios a correct minimal cell satisfies.`

// uiScenarios authors behavioral draw-stream scenarios for a UI subsystem. If
// the model produces nothing usable, it falls back to a deterministic floor: a
// UI cell must render at least one primitive (so a blank canvas always fails).
func (g *Grower) uiScenarios(ctx context.Context, sub Subsystem) *evolution.AcceptanceSuite {
	floor := &evolution.AcceptanceSuite{Scenarios: []evolution.Scenario{{
		Name:  "renders at least one primitive",
		Steps: 1, Entry: "render-frame",
		Expect: evolution.ScenarioExpect{Draw: &evolution.DrawExpect{MinRecords: 1}},
	}}}
	resp, err := g.model.InvokeReasoning(ctx, g.prompt("scenario", scenarioPrompt), sub.Semantics)
	if err != nil {
		return floor
	}
	js := extractJSON(resp)
	if js == "" {
		return floor
	}
	var suite evolution.AcceptanceSuite
	if json.Unmarshal([]byte(js), &suite) != nil || len(suite.Scenarios) == 0 {
		return floor
	}
	// Ground the model-authored draw scenarios against the shared-state contract,
	// exactly as the coordination path does: promote a renderer's eq
	// reads-postconditions into seeds, drop unseeded/out-of-bounds position asserts,
	// and drop any non-render check. Without this, a correct renderer that draws at
	// (dot_x,dot_y) still fails a check that asserts a position it never seeded.
	// This is a UI cell, so isUI=true.
	if c := evolution.LoadContract(g.ledger, evolution.AppNamespaceOf(sub.Identity)); c != nil {
		suite.Scenarios = groundScenarios(suite.Scenarios, c, true)
	}
	// Always keep the deterministic floor alongside model-authored scenarios.
	suite.Scenarios = append(suite.Scenarios, floor.Scenarios...)
	return &suite
}

const genesisSystem = `SYSTEM ROLE: HDM GENESIS SYNTHESIS. Produce the SIMPLEST correct WAT cell that
does the ONE thing described — NOT the whole application. This is the first,
minimal version; the system builds up in small steps and the annealing loop
refines it later. A crude, minimal cell that RUNS beats an ambitious one that
does not compile.

For this first version:
- Implement only the single behavior in the subsystem semantics. Stub or hard-code
  everything else. Do NOT add features that belong to other subsystems.
- Prefer the smallest program that compiles and runs: few locals, at most one loop,
  no cleverness.
- For a UI cell, render a minimal but recognizable first frame (e.g. a handful of
  rects at fixed positions) rather than a full animated scene.

Output ONLY a single (module ...) form — no prose, no markdown fences.

` + evolution.Capabilities

// genesis synthesizes a crude WAT skeleton for a subsystem, choosing the UI
// (render-frame) or compute (run-tick) contract from its semantics, and falling
// back to a minimal valid skeleton if the model cannot produce a compilable one.
func (g *Grower) genesis(ctx context.Context, env *AppEnvelope, sub Subsystem, wit string) (string, []byte) {
	// Macro-WAT mode: seed a deterministic no-op macro-WAT cell so the genome is in
	// the authoring surface from birth and the synthesis loop iterates on a real draft
	// rather than a WAT skeleton. Falls through to WAT genesis when the app has no
	// contract-addressable layout (or the no-op doesn't assemble).
	if g.FluxEnabled {
		if src, bc, ok := g.seedNoopMacro(env, sub); ok {
			return src, bc
		}
	}
	ui := kindOf(sub) == KindRender
	contract := `export a function named exactly "run-tick" with signature (param i32 i32) (result i32)`
	if ui {
		contract = `export a UI function named exactly "render-frame" with signature (param i32 i32) (result i32): write a vector draw stream at the first parameter (base) and return the byte length (see the UI cell contract above)`
	}
	seed := fmt.Sprintf(`Synthesize subsystem %s of application %s.
Semantics: %s

Interface contract (WIT):
%s

Requirements:
- import (memory 100) from module "hdm:kernel/hardware-io" export "shared-cluster-memory".
- Implement ONLY the one behavior in Semantics — the smallest thing that runs.
  Anything beyond that is another subsystem's job, not this cell's.
- %s.`, sub.Identity, env.ApplicationNamespace, sub.Semantics, wit, contract)

	sig := evolution.RunTickContract
	if ui {
		sig = evolution.RenderFrameContract
	}
	out, err := evolution.RunSieve(ctx, g.modelFor(deriveModelType(kindOf(sub))), genesisSystem, seed, g.SieveIter, sig)
	if err == nil && out != nil && out.Artifact != nil && out.Artifact.SyntaxPassed {
		return out.WAT, out.Artifact.Bytecode
	}
	fb := fallbackSkeleton
	if ui {
		fb = uiFallbackSkeleton
	}
	art, _ := g.sieve.CompileGenotype(fb)
	return fb, art.Bytecode
}

// uiFallbackSkeleton draws a single placeholder rectangle so a UI subsystem
// renders something (not a blank canvas) even if genesis synthesis fails.
const uiFallbackSkeleton = `(module
  (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 100))
  (func (export "render-frame") (param $base i32) (param $cap i32) (result i32)
    local.get $base i32.const 1 i32.store
    local.get $base i32.const 4 i32.add i32.const 20 i32.store
    local.get $base i32.const 8 i32.add i32.const 20 i32.store
    local.get $base i32.const 12 i32.add i32.const 80 i32.store
    local.get $base i32.const 16 i32.add i32.const 80 i32.store
    local.get $base i32.const 20 i32.add i32.const 0x3A6EA5FF i32.store
    i32.const 24))`

// IsUISubsystem is the LIVE-CELL role heuristic: it guesses whether a running
// cell renders, from its semantic-intent text. It is the fallback used at runtime
// sites that hold only a built cell's FunctionalIntent (not its Subsystem) — where
// the intent already reflects the cell's actual role. In the AUTHORING path, the
// declared CellKind (validated against ports) is authoritative instead; prefer
// Subsystem.IsRender / kindOf there.
func IsUISubsystem(semantics string) bool { return isUISubsystem(semantics) }

// isUISubsystem guesses whether a subsystem renders, from its semantics text.
// Kept only as the keyword FALLBACK inside deriveKind's cousins and the live-cell
// heuristic above — the ontology (kind.go) supersedes it in the authoring path.
func isUISubsystem(semantics string) bool {
	s := strings.ToLower(semantics)
	for _, kw := range []string{"render", "ui", "canvas", "draw", "visual", "display", "paint", "graphic", "screen", "view", "frontend", "front-end"} {
		if strings.Contains(s, kw) {
			return true
		}
	}
	return false
}

// isInitSubsystem reports whether a subsystem's role is INITIALIZATION — seeding the
// shared state to its starting values. Such a cell is compute (run-tick), never a
// renderer, even if it under-declared its write ports or its semantics also mention the
// "screen". The verb forms are deliberately specific so a view that merely "draws the
// initial screen" is not swept in (it says draw/render, not initialize).
func isInitSubsystem(semantics string) bool {
	s := strings.ToLower(semantics)
	for _, kw := range []string{"initializ", "initialis", "seed the initial", "set up the initial"} {
		if strings.Contains(s, kw) {
			return true
		}
	}
	return false
}

// extractWIT isolates the WIT text from a completion, dropping fences/prose by
// starting at the "package " declaration when present.
func extractWIT(resp string) string {
	if i := strings.Index(resp, "package "); i >= 0 {
		return strings.TrimSpace(strings.ReplaceAll(resp[i:], "```", ""))
	}
	return strings.TrimSpace(strings.ReplaceAll(resp, "```", ""))
}

// witPackage derives a WIT package identifier from an application namespace.
func witPackage(ns string) string {
	s := strings.TrimPrefix(ns, "urn:hdm:apps:")
	s = strings.ReplaceAll(s, ":", "-")
	if s == "" {
		s = "app"
	}
	return "hdm:apps/" + s + "@1.0.0"
}

// nsTag derives a short domain tag from an application namespace.
func nsTag(ns string) string {
	parts := strings.Split(ns, ":")
	if len(parts) == 0 {
		return "app"
	}
	return parts[len(parts)-1]
}

// extractJSON isolates the first balanced {...} object in a string.
func extractJSON(s string) string {
	start := strings.IndexByte(s, '{')
	if start < 0 {
		return ""
	}
	depth, inStr, esc := 0, false, false
	for i := start; i < len(s); i++ {
		c := s[i]
		switch {
		case esc:
			esc = false
		case c == '\\' && inStr:
			esc = true
		case c == '"':
			inStr = !inStr
		case inStr:
		case c == '{':
			depth++
		case c == '}':
			depth--
			if depth == 0 {
				return s[start : i+1]
			}
		}
	}
	return ""
}

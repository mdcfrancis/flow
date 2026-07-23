package evolution

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/mdcfrancis/flow/codependency"
	"github.com/mdcfrancis/flow/compiler"
	"github.com/mdcfrancis/flow/engine"
	"github.com/mdcfrancis/flow/inference"
	"github.com/mdcfrancis/flow/manifest"
	"github.com/mdcfrancis/flow/storage"
	"github.com/mdcfrancis/flow/tapes"
)

// DefaultCompassPrompt is the strategic compass registry seed steering the
// cognitive engine toward lower-energy, behavior-preserving mutations.
// Capabilities documents the Monadic Host Services, shared-memory layout, and
// cell entry contracts a guest cell may target. It is injected into synthesis
// prompts so the model knows the ABI (e.g. that render-frame + the draw-stream
// format exist for UI cells).
const Capabilities = `HDM HOST SERVICES — import from these modules (guest cells may NOT open sockets
or touch hardware directly; route everything through these host functions):
- hdm:kernel/hardware-io   : (memory 100) exported as "shared-cluster-memory"
- hdm:kernel/cell-dispatch : invoke-cell(urn_ptr i32, urn_len i32, arg_ptr i32, arg_len i32) -> i32   (call another cell by URN)
- hdm:kernel/block-storage : read-block / write-block / get-ref / update-ref   (content-addressed blocks + refs)
- hdm:kernel/cognitive-engine : invoke-reasoning(sys_ptr, sys_len, ctx_ptr, ctx_len) -> (i32, i32)
- hdm:kernel/chronos       : now-ns() -> i64, tick() -> i64 (monotonic heartbeat beat), random() -> i64 (pseudo-random stream), entropy(index i32) -> i32   (route ALL time/randomness through chronos — never ambient)
- hdm:kernel/cell-logger   : emit-log(level i32, msg_ptr i32, msg_len i32)

SHARED MEMORY LAYOUT (offsets into shared-cluster-memory):
- 0x00000..0x0FFFF  system pointer registry (read-only)
- 0x10000..0x4FFFF  inbound packet frame (edge input)
- 0x50000..0x50FFF  HMI input event register (keyboard/mouse; read-only to cells)
- 0x51000..0xAFFFF  canvas UI draw-output buffer
- 0xB0000..end      dynamic sandbox — scratch/state that PERSISTS across ticks

HMI INPUT EVENT REGISTER (read these u32 fields to react to the operator):
- 0x50000 mouseX (i32, 0..319)      0x50004 mouseY (i32, 0..239)
- 0x50008 buttons mask              0x5000C modifiers mask (bit0 shift..bit3 meta)
- 0x50010 eventSeq (monotonic; compare to your last-seen value to spot a new event)
- 0x50014 eventType (2 down, 3 up, 4 click, 5 keydown, 6 keyup)
- 0x50018 eventX  0x5001C eventY  0x50020 keyCode
  To detect a click: stash eventSeq in your sandbox each tick; when it changes
  and eventType is 4/2, act on eventX/eventY. Mouse moves do NOT bump eventSeq.

CELL ENTRY CONTRACTS — export exactly the one that fits the cell's role:
- Compute cell : run-tick(ptr i32, len i32) -> i32
- Router cell  : route-packet(ptr i32, len i32) -> i32   (ptr/len address the inbound frame)
- UI cell      : render-frame(base i32, cap i32) -> i32  — write a VECTOR DRAW STREAM
    starting at 'base' and RETURN the number of bytes written. The stream is a
    sequence of fixed 24-byte little-endian records [op i32, a i32, b i32, c i32, d i32, rgba i32]:
      op low byte  = primitive: 1 rect(x=a,y=b,w=c,h=d), 2 line(x1=a,y1=b,x2=c,y2=d), 3 circle(cx=a,cy=b,r=c)
      op high byte = compositing layer: 0 static basemap, 1 widget/app canvas, 2 reserved for host overlay/cursor
    so op = (layer<<8)|primitive; a bare op of 1/2/3 paints on layer 0. rgba is
    packed 0xRRGGBBAA; the canvas is 320x240. Keep persistent state (grids,
    counters) in the dynamic sandbox region so it survives between frames.`

const DefaultCompassPrompt = `SYSTEM ROLE: HDM REFACTORING CORE.
Objective: minimize the cluster Hamiltonian energy (latency, fuel, compute cost)
without altering observable behavior.

You are given the current WAT genotype of a cell. Emit an optimized replacement.

HARD CONTRACT — a violation causes immediate rejection:
1. Export a function named exactly "run-tick" with signature
   (param i32 i32) (result i32). Do NOT add, remove, or reorder its parameters
   or results.
2. Preserve every import exactly as written (same module, name, and signature).
   Do not add new imports.
3. For any given input the module MUST return the identical result and leave
   memory in the identical state as the original — it is replayed against a
   regression tape and any bit-level divergence is rejected.
4. The only permitted change is to consume LESS execution fuel (fewer executed
   instructions / function calls) while doing 1–3.
5. NEVER remove or reduce the number of calls to an effectful host function —
   above all hdm:kernel/cognitive-engine/invoke-reasoning. These have external
   side effects that replay CANNOT verify, so the sandbox may show their result
   unused (dropped) — they are NOT dead code. Removing even one such call is an
   immediate rejection. Keep every invoke-reasoning call exactly as in the
   original; optimize only the surrounding non-effectful work.

OUTPUT: only a single complete, valid WebAssembly Text (WAT) (module ...) form.
No prose, no markdown fences, no commentary.

` + Capabilities

// exampleRunTickWAT and exampleRenderFrameWAT are minimal, valid worked examples
// fed to the builder as few-shot guidance. They use the same linear (stack-form)
// style the hand-written cells compile with, so the model has a concrete,
// parseable structure to imitate — directly targeting the "invalid WAT" failure
// where a model can't balance parens. A test asserts they compile.
const exampleRunTickWAT = `(module
  (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 100))
  (func (export "run-tick") (param $ptr i32) (param $len i32) (result i32)
    local.get $ptr i32.load8_u))`

const exampleRenderFrameWAT = `(module
  (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 100))
  (func (export "render-frame") (param $base i32) (param $cap i32) (result i32)
    local.get $base i32.const 257 i32.store
    local.get $base i32.const 4 i32.add i32.const 20 i32.store
    local.get $base i32.const 8 i32.add i32.const 30 i32.store
    local.get $base i32.const 12 i32.add i32.const 8 i32.store
    local.get $base i32.const 16 i32.add i32.const 8 i32.store
    local.get $base i32.const 20 i32.add i32.const 0x33FF66FF i32.store
    i32.const 24))`

const buildExamples = `
WORKED EXAMPLES — valid WAT in the exact style to imitate (linear/stack form,
balanced parens, one function). Match this structure:

Compute cell (run-tick) — returns the first inbound byte:
` + exampleRunTickWAT + `

UI cell (render-frame) — draws one rect on layer 1 (op=(layer<<8)|1=257) into a
24-byte record at 'base' and returns the byte length:
` + exampleRenderFrameWAT + `
`

// DefaultBuildPrompt steers spec-driven BUILDING (as opposed to optimization).
// Unlike the compass, it explicitly permits changing behavior to satisfy the
// acceptance checks — that is the whole point of building. It is entry-agnostic:
// the cell may export run-tick (compute) or render-frame (UI).
const DefaultBuildPrompt = `SYSTEM ROLE: HDM BUILDER.
You are given a cell's PLAN (the design to implement), the system it is part of,
its current WAT genotype, and its ACCEPTANCE CHECKS. IMPLEMENT THE PLAN: write the
cell's algorithm exactly as the plan's steps describe, reading and writing the
shared fields it names. The acceptance checks VERIFY the plan — a correct
implementation of the plan passes them; if a check seems to disagree with the
plan, satisfy the check. Pass AS MANY CHECKS AS POSSIBLE.

Unlike optimization, you MAY and SHOULD change observable behavior to satisfy the
spec — build the behavior the checks describe. Keep the exact required entry
export and its signature; keep every import; keep persistent state in the
sandbox region so it survives between ticks.

OUTPUT: only a single complete, valid WebAssembly Text (WAT) (module ...) form.
No prose, no markdown fences.
` + buildExamples + `
` + Capabilities

const (
	// EntryPoint is the exported function the scheduler drives on each cell.
	EntryPoint = "run-tick"
	// CompassURN is the mutable ledger reference holding the strategic compass
	// prompt, so the steering prompt is itself evolvable.
	CompassURN = "urn:hdm:sys:prompts:compass"
)

// Orchestrator drives the evolutionary mutation frame against the ledger.
type Orchestrator struct {
	ledger        *storage.LedgerEngine
	repo          *manifest.Repository
	mvcc          *engine.MVCCCoordinator
	model         Reasoner
	SieveMaxIters int
	CompassPrompt string
	// TapeCount caps the regression corpus size; PoolSize is how many candidate
	// inputs are probed to fill it. The Discovery-Invariant compactor keeps only
	// inputs that reveal new behavior, so kept <= TapeCount <= PoolSize.
	TapeCount     int
	PoolSize      int
	PayloadOffset uint32
	StateWindow   uint32
	// TokenMilliCents / Saliency parameterize the Hamiltonian scoring.
	TokenMilliCents uint32
	Saliency        float64
	// MinEnergyDrop is the minimum Hamiltonian reduction a candidate must
	// achieve to be accepted (0 => any strict improvement).
	MinEnergyDrop float64
	// Topology triggers.
	GravityThreshold   float64
	LatencyThresholdNS uint64
	// Chaos, when Active, gates every hot-swap on the candidate surviving an
	// adversarial replay.
	Chaos ChaosProfile
	// Gravity, when set, records co-mutation data. Optional.
	Gravity *codependency.Tracker
	// lastLatency tracks each cell's most recent baseline p99 bridge latency,
	// feeding the Fusion trigger.
	lastLatency map[string]uint64
	// escalation counts a cell's consecutive no-acceptance-progress synthesis
	// attempts. Past sieveEscalateThreshold the sieve escalates its model type to
	// the reasoner (the cheaper kind-derived model couldn't crack it) — cost-driven
	// escalation. Reset on any commit (progress). Serial with the evolution loop.
	escalation map[string]int
	// Tapes, when set, persists each frame's discovered regression corpus to
	// the CAS ledger so the Tape Compaction Janitor has a repository to prune.
	Tapes *TapeStore
	// Activity, when set, receives phase transitions and cell-lifecycle events
	// during a frame so an operator surface can show what the loop is doing and
	// (during a model round-trip) why it is waiting. Optional; nil-safe.
	Activity ActivitySink
	// SieveModelType, when set, maps a cell URN to the model type that should
	// SYNTHESIZE it (from its cell kind) — so the evolution-loop sieve routes a render
	// cell to the vision model and compute/leaf cells to the coder. Injected from
	// main (appgen.ModelTypeForCell) to avoid an evolution→appgen import cycle. When
	// nil the sieve uses the code type. Optional; nil-safe.
	SieveModelType func(urn string) inference.ModelType
	// structural marks cells whose LOCAL optimization has plateaued while they are
	// still expensive: their next synthesis is offered the data-structure toolkit
	// (StructureToolkit) so the model may refactor to dispatch to a shared primitive
	// (or mint one) instead of micro-optimizing a loop. The verification gates are
	// unchanged, so a structural refactor is accepted only if behavior holds and cost
	// drops. Set by the scheduler on plateau, cleared on a structural commit.
	structural map[string]bool
}

// SetStructural flags (or clears) a cell for structural escalation — its next synthesis
// unlocks the data-structure action space. Called by the scheduler when local optimization
// plateaus on a still-expensive cell.
func (o *Orchestrator) SetStructural(urn string, on bool) {
	if o.structural == nil {
		o.structural = map[string]bool{}
	}
	if on {
		o.structural[urn] = true
	} else {
		delete(o.structural, urn)
	}
}

// IsStructural reports whether a cell is currently flagged for structural escalation.
func (o *Orchestrator) IsStructural(urn string) bool { return o.structural[urn] }

// ActivitySink receives coarse progress signals from the mutation loop. It is
// deliberately narrow so the evolution package need not import a concrete UI
// broker (status.Broker satisfies it structurally).
type ActivitySink interface {
	// Phase records the current activity and, when blocked, why it is waiting.
	Phase(phase, reason, target string)
	// Event records a discrete lifecycle event (mutate/fission/fusion/commit…).
	Event(kind, cell, detail string)
}

// phase / event are nil-safe shims so instrumentation calls stay terse.
func (o *Orchestrator) phase(name, reason, target string) {
	if o.Activity != nil {
		o.Activity.Phase(name, reason, target)
	}
}

func (o *Orchestrator) event(kind, cell, detail string) {
	if o.Activity != nil {
		o.Activity.Event(kind, cell, detail)
	}
}

// NewOrchestrator constructs an orchestrator bound to a ledger and cognitive
// engine.
func NewOrchestrator(ledger *storage.LedgerEngine, model Reasoner) *Orchestrator {
	return &Orchestrator{
		ledger:             ledger,
		repo:               manifest.NewRepository(ledger),
		mvcc:               engine.NewMVCCCoordinator(ledger),
		model:              model,
		SieveMaxIters:      5,
		CompassPrompt:      DefaultCompassPrompt,
		TapeCount:          8,
		PoolSize:           32,
		PayloadOffset:      DefaultPayloadOffset,
		StateWindow:        DefaultStateWindow,
		TokenMilliCents:    0,
		Saliency:           0,
		GravityThreshold:   DefaultGravityThreshold,
		LatencyThresholdNS: DefaultBridgeLatencyThresholdNS,
		lastLatency:        map[string]uint64{},
		escalation:         map[string]int{},
		// Default chaos: clock drift, which rejects candidates whose behavior
		// depends on wall time. Memory-boundary clamping and dropped host
		// signals are off by default — the former needs B6 clamping hooks
		// (deferred; clamping the shared memory below the imported 100 pages
		// merely breaks instantiation), the latter breaks legitimately
		// dispatch-dependent cells such as fission dispatchers.
		Chaos: ChaosProfile{ClockDriftNS: 1_000_000_000},
	}
}

// chaosOK gates a candidate on surviving the adversarial replay, if chaos is
// active. Returns (true, "") when chaos is disabled.
func (o *Orchestrator) chaosOK(ctx context.Context, candidate []byte, cases []RegressionCase, resolver CellResolver) (bool, string) {
	if !o.Chaos.Active() {
		return true, ""
	}
	return SurvivesChaos(ctx, candidate, EntryPoint, cases, o.PayloadOffset, o.StateWindow, o.Chaos, resolver)
}

// Repository exposes the descriptor repository for seeding and inspection.
func (o *Orchestrator) Repository() *manifest.Repository { return o.repo }

// ManifestRoot returns the current global manifest root hash — the system-state
// key that persistent convergence is recorded against. It advances on every
// committed mutation, so a change here means the system context moved.
func (o *Orchestrator) ManifestRoot() string {
	root, _ := o.mvcc.InitManifest()
	return root
}

// RecertifySuite re-judges a cell's acceptance suite against its required
// behavior (the descriptor's FunctionalIntent) through the adversarial auditor,
// and persists the certified (faithful) subset. This is the always-on gate for
// suite evolution: whatever proposed the change (an expander adding tests, an
// operator editing the objective), the suite is only whatever the auditor
// certifies as faithful to the requirement. Returns how many checks were kept vs
// dropped. A no-op (and nil error) when the cell has no suite.
func (o *Orchestrator) RecertifySuite(ctx context.Context, cellURN string) (kept, dropped int, err error) {
	suite, _ := LoadAcceptance(o.ledger, cellURN)
	if suite == nil || (len(suite.Tests) == 0 && len(suite.Scenarios) == 0) {
		return 0, 0, nil
	}
	before := len(suite.Tests) + len(suite.Scenarios)
	desc, err := o.repo.Load(cellURN)
	if err != nil {
		return 0, 0, fmt.Errorf("load descriptor: %w", err)
	}
	requirement := desc.Semantics.FunctionalIntent
	certified, verdicts, err := ValidateSuite(ctx, o.model, requirement, suite)
	if err != nil {
		return 0, 0, err // fail-open: caller keeps the existing suite
	}
	after := len(certified.Tests) + len(certified.Scenarios)
	if after == before {
		return after, 0, nil // nothing dropped; leave the stored suite as-is
	}
	for _, v := range verdicts {
		if !v.Faithful {
			o.event("reject", cellURN, "test dropped: "+v.Name+" — "+v.Reason)
		}
	}
	if err := SaveAcceptance(o.ledger, cellURN, certified); err != nil {
		return 0, 0, fmt.Errorf("persist recertified suite: %w", err)
	}
	return after, before - after, nil
}

// ScoreCell reports how many acceptance checks the cell's current live phenotype
// passes; total is 0 when the cell has no acceptance suite. The staged growth
// pipeline uses this to decide when a stage is complete (passed == total).
func (o *Orchestrator) ScoreCell(ctx context.Context, cellURN string) (passed, total int, err error) {
	suite, _ := LoadAcceptance(o.ledger, cellURN)
	if suite == nil {
		return 0, 0, nil
	}
	desc, err := o.repo.Load(cellURN)
	if err != nil {
		return 0, 0, fmt.Errorf("load descriptor: %w", err)
	}
	phenotype, err := o.repo.Phenotype(desc)
	if err != nil {
		return 0, 0, fmt.Errorf("load phenotype: %w", err)
	}
	p, t := ScoreSuite(ctx, phenotype, EntryPoint, suite, o.PayloadOffset, o.StateWindow, o.resolver())
	return p, t, nil
}

// ScenarioResult is one coordination scenario's read-only status, for inspection.
type ScenarioResult struct {
	Name string `json:"name"`
	Pass bool   `json:"pass"`
}

// ScenarioFlags returns, per scenario, whether it currently passes against the
// cell's live genotype — so an inspector can show WHICH specific coordination
// check fails, not just the aggregate score. Read-only; nil if the cell has no
// scenarios or can't be resolved.
func (o *Orchestrator) ScenarioFlags(ctx context.Context, cellURN string) ([]ScenarioResult, error) {
	suite, _ := LoadAcceptance(o.ledger, cellURN)
	if suite == nil || len(suite.Scenarios) == 0 {
		return nil, nil
	}
	desc, err := o.repo.Load(cellURN)
	if err != nil {
		return nil, err
	}
	phenotype, err := o.repo.Phenotype(desc)
	if err != nil {
		return nil, err
	}
	flags := ScenarioPassFlags(ctx, phenotype, suite.Scenarios, o.PayloadOffset, o.StateWindow, o.resolver())
	out := make([]ScenarioResult, 0, len(suite.Scenarios))
	for i, s := range suite.Scenarios {
		pass := i < len(flags) && flags[i]
		out = append(out, ScenarioResult{Name: s.Name, Pass: pass})
	}
	return out, nil
}

// resolver returns a CellResolver backed by the descriptor repository, so shadow
// replays can follow inter-cell dispatch (needed for fusion/fission baselines).
// modelFor selects the client bound to a logical model type when the model is a
// router, else the single model. The sieve routes to code; other calls use o.model,
// which the router resolves to the reason type by default.
func (o *Orchestrator) modelFor(mt inference.ModelType) Reasoner {
	if r, ok := o.model.(*inference.ModelRouter); ok {
		return r.For(string(mt))
	}
	return o.model
}

// sieveEscalateThreshold is how many consecutive no-progress synthesis attempts a
// cell may accrue before its sieve escalates to the reasoner.
const sieveEscalateThreshold = 2

// sieveModel picks the synthesis client for a target: its kind-derived model type
// when a resolver is wired, else the code type — but a cell that has repeatedly
// STALLED escalates to the reasoner, the strongest model. The escalation is
// cost-justified: the cheaper model already failed to crack this cell.
func (o *Orchestrator) sieveModel(urn string) Reasoner {
	mt := inference.ModelCode
	if o.SieveModelType != nil {
		mt = o.SieveModelType(urn)
	}
	if o.escalation[urn] >= sieveEscalateThreshold {
		mt = inference.ModelReason
	}
	return o.modelFor(mt)
}

func (o *Orchestrator) resolver() CellResolver {
	return func(urn string) ([]byte, bool) {
		desc, err := o.repo.Load(urn)
		if err != nil {
			return nil, false
		}
		bc, err := o.repo.Phenotype(desc)
		if err != nil {
			return nil, false
		}
		return bc, true
	}
}

// compass returns the active strategic compass prompt, seeding the registry
// with the default on first use so it becomes a mutable, evolvable reference.
func (o *Orchestrator) compass() string {
	if h, err := o.ledger.GetRef(CompassURN); err == nil {
		if b, err := o.ledger.ReadBlock(h); err == nil {
			return string(b)
		}
	}
	if h, err := o.ledger.WriteBlock([]byte(o.CompassPrompt)); err == nil {
		_ = o.ledger.UpdateRef(CompassURN, h)
	}
	return o.CompassPrompt
}

// FrameResult is the outcome of a single evolutionary mutation frame.
type FrameResult struct {
	TargetURN string
	Kind      TopologyMutationKind
	Attempted bool          // a candidate was synthesized and graded
	Committed bool          // the candidate was hot-swapped into the ledger
	Transport bool          // synthesis failed because the model server was unreachable (infra, not the cell)
	NewRoot   string        // manifest root after a successful commit
	Verdict   *Verdict      // gauntlet ruling (nil if synthesis failed)
	Sieve     *SieveOutcome // synthesis result (nil if reasoning failed)
	Reason    string        // human-readable frame summary

	CorpusKept       int // regression cases retained after compaction
	CorpusConsidered int // candidate inputs probed by the compactor

	// Acceptance-suite scores (spec-driven growth); AcceptTotal == 0 means the
	// cell has no acceptance suite.
	AcceptBase  int
	AcceptCand  int
	AcceptTotal int

	// MintedPrimitive is set (to its URN) when a structural refactor COMMITTED while
	// depending on a newly-minted data-structure primitive — the primitive is then
	// witnessed correct by this cell reproducing its tapes, and the scheduler enrolls it.
	MintedPrimitive string
}

// RunFrame executes one complete mutation frame for targetURN following the
// Lifecycle: context ingestion (load the descriptor + its genotype and
// phenotype), sieve synthesis (refine the real WAT genotype), gauntlet
// verification (tape replay + Hamiltonian scoring), and atomic MVCC commit of a
// new descriptor. Expected conditions (cognitive engine offline, rejected
// candidate) surface in FrameResult rather than as errors.
func (o *Orchestrator) RunFrame(ctx context.Context, targetURN string) (*FrameResult, error) {
	fr := &FrameResult{TargetURN: targetURN}

	// 1. Context ingestion: pin the base manifest root and load the active
	//    descriptor with its genotype (WAT) and phenotype (bytecode).
	baseRoot, err := o.mvcc.InitManifest()
	if err != nil {
		return nil, fmt.Errorf("manifest anchor: %w", err)
	}
	desc, err := o.repo.Load(targetURN)
	if err != nil {
		return nil, fmt.Errorf("load descriptor: %w", err)
	}
	genotype, err := o.repo.Genotype(desc)
	if err != nil {
		return nil, fmt.Errorf("load genotype: %w", err)
	}
	baseline, err := o.repo.Phenotype(desc)
	if err != nil {
		return nil, fmt.Errorf("load phenotype: %w", err)
	}

	// 2. Sieve synthesis. The cell is evolved against WHICHEVER monadic entry it
	//    implements (run-tick for compute, render-frame for UI), and a cell with
	//    an acceptance suite is BUILT toward its spec (behavior may change),
	//    whereas a suiteless cell is OPTIMIZED (behavior preserved).
	contract := entryContractFor(genotype)
	suite, _ := LoadAcceptance(o.ledger, targetURN)
	building := suite != nil && (len(suite.Tests) > 0 || len(suite.Scenarios) > 0)

	// STRUCTURAL ESCALATION takes priority: a flagged cell is REFACTORED (not rebuilt). Its
	// seed leads with the working genotype and "preserve exact behavior", then offers the
	// data-structure toolkit — otherwise the build prompt makes the model rewrite from scratch
	// and lose the behavior. The verification gates below are unchanged.
	structural := o.structural[targetURN]
	var sysPrompt, seed string
	switch {
	case structural:
		sysPrompt = ResolvePrompt(o.ledger, "build", DefaultBuildPrompt)
		seed = o.structuralSeed(targetURN, contract, genotype, suite)
		o.event("mutate", targetURN, "structural escalation — refactor to a data structure")
		o.phase("synthesizing", "structural refactor of "+targetURN+" (awaiting cognitive engine)", targetURN)
	case building:
		sysPrompt = ResolvePrompt(o.ledger, "build", DefaultBuildPrompt)
		seed = o.buildSeed(targetURN, desc.Semantics.FunctionalIntent, genotype, contract, suite)
		o.event("mutate", targetURN, "building toward spec ("+contract.Name+")")
		o.phase("synthesizing", "building a candidate for "+targetURN+" (awaiting cognitive engine)", targetURN)
	default:
		sysPrompt = o.compass()
		seed = fmt.Sprintf("Optimize cell %s (entry %s). Current genotype:\n\n%s", targetURN, contract.Name, genotype)
		o.event("mutate", targetURN, "genotype refinement")
		o.phase("synthesizing", "reasoning a candidate for "+targetURN+" (awaiting cognitive engine)", targetURN)
	}
	sieve, serr := RunSieve(ctx, o.sieveModel(targetURN), sysPrompt, seed, o.SieveMaxIters, contract)
	// NOVEL-PRIMITIVE MINTING (witnessed by consumer): if the structural response minted a new
	// primitive, provisionally store it so the refactored cell can dispatch to it during
	// verification. The primitive is TRUSTED only if this cell commits (its tapes hold while
	// using it); otherwise it is retired. The defer finalizes that decision on any return path.
	var mintedURN string
	if structural && serr == nil && sieve != nil {
		if purn, pwat, ok := extractNewPrimitive(sieve.Raw); ok && o.provisionMint(purn, pwat) {
			mintedURN = purn
			o.event("mutate", targetURN, "minted candidate primitive "+purn+" (pending witness)")
		}
	}
	defer func() {
		if mintedURN == "" {
			return
		}
		if fr.Committed {
			fr.MintedPrimitive = mintedURN // witnessed correct by this cell's tapes
		} else {
			o.retireMint(mintedURN) // never witnessed — remove the untrusted provisional cell
		}
	}()
	if serr != nil {
		fr.Sieve = sieve
		fr.Reason = fmt.Sprintf("synthesis skipped: %v", serr)
		// A model-server outage is infrastructure, not a failure of the cell — flag
		// it so the scheduler does not count it as a convergence hold (which would
		// wrongly park the cell just because the model blipped).
		fr.Transport = errors.Is(serr, inference.ErrServerUnreachable)
		return fr, nil
	}
	fr.Sieve = sieve
	fr.Attempted = true
	if o.Gravity != nil {
		// Co-mutation tracking: note this isolation optimization pass on the
		// target. Joint failures are recorded when a committed mutation
		// is later observed to regress a dependent cell.
		_ = o.Gravity.RecordIsolationAttempt(targetURN)
	}

	// Spec-driven growth: prioritize correctness progress over behavior
	// preservation (works for run-tick and render-frame cells alike).
	if building {
		return o.acceptanceFrame(ctx, fr, baseRoot, targetURN, desc, baseline, sieve, suite, contract)
	}

	// Structural invariant: a candidate must not drop effectful host calls
	// (e.g. LLM reasoning). Their external side effects cannot be verified by
	// deterministic replay, so removal is forbidden regardless of observable
	// equivalence in the sandbox.
	for _, eff := range desc.Semantics.EffectfulImports {
		baseN, _ := compiler.CountImportCalls(genotype, eff.Module, eff.Name)
		candN, cErr := compiler.CountImportCalls(sieve.WAT, eff.Module, eff.Name)
		if cErr != nil || candN < baseN {
			fr.Reason = fmt.Sprintf("rejected: candidate drops effectful call %s/%s (%d -> %d)",
				eff.Module, eff.Name, baseN, candN)
			return fr, nil
		}
	}

	// 3. Gauntlet verification: compact a regression corpus from the live
	//    baseline (keeping only Discovery-Invariant-worthy inputs), then stream
	//    it through the candidate.
	cases, considered, cerr := o.compactCorpus(ctx, baseline)
	if cerr != nil {
		return nil, fmt.Errorf("compact regression corpus: %w", cerr)
	}
	fr.CorpusKept = len(cases)
	fr.CorpusConsidered = considered
	if o.Tapes != nil {
		frames := make([]*tapes.TransactionFrame, len(cases))
		for i, rc := range cases {
			frames[i] = rc.Frame
		}
		_ = o.Tapes.Append(targetURN, frames)
	}
	o.phase("grading", "replaying regression tapes for "+targetURN, targetURN)
	verdict, verr := RunGauntletCases(ctx, baseline, sieve.Artifact.Bytecode, EntryPoint,
		cases, o.PayloadOffset, o.StateWindow, o.TokenMilliCents, o.Saliency, o.MinEnergyDrop, o.resolver())
	if verr != nil {
		fr.Reason = fmt.Sprintf("gauntlet error: %v", verr)
		return fr, nil
	}
	fr.Verdict = verdict
	if o.lastLatency != nil {
		o.lastLatency[targetURN] = verdict.BaselineLatencyP99NS
	}
	if !verdict.Accepted {
		// A candidate that PRESERVED behavior but did not lower energy is HELD, not rejected —
		// it is a valid, equivalent cell that simply is not an improvement. Only a behavioral
		// divergence is a true rejection.
		if verdict.OutputMatch {
			fr.Reason = "held (equivalent; no energy improvement): " + verdict.Reason
		} else {
			fr.Reason = "rejected: " + verdict.Reason
		}
		return fr, nil
	}
	if ok, reason := o.chaosOK(ctx, sieve.Artifact.Bytecode, cases, o.resolver()); !ok {
		fr.Reason = "chaos rejected: " + reason
		return fr, nil
	}

	// 4. Atomic reference commit: persist the new genotype+phenotype as a fresh
	//    descriptor and hot-swap under an optimistic concurrency check.
	newDescHash, _, err := o.repo.PutCell(targetURN, sieve.WAT, sieve.Artifact.Bytecode, desc.Semantics, desc.Saliency)
	if err != nil {
		return nil, fmt.Errorf("persist evolved descriptor: %w", err)
	}
	newRoot, cmErr := o.mvcc.ProposeEvolutionCommit(ctx, baseRoot, targetURN, newDescHash)
	if cmErr != nil {
		fr.Reason = fmt.Sprintf("commit aborted: %v", cmErr)
		return fr, nil
	}

	fr.Committed = true
	fr.NewRoot = newRoot
	fr.Reason = fmt.Sprintf("evolved across %d tapes: fuel %d->%d, H %.4f->%.4f",
		verdict.TapesMatched, verdict.BaselineFuel, verdict.CandidateFuel, verdict.BaselineH, verdict.CandidateH)
	return fr, nil
}

// compactCorpus probes a pool of candidate inputs against the baseline and,
// via the Discovery-Invariant compactor, retains only those that reveal new
// behavior — new code coverage, a new scalar extremum, or a fault path — up to
// TapeCount. It returns the kept cases and how many inputs were considered.
func (o *Orchestrator) compactCorpus(ctx context.Context, baseline []byte) ([]RegressionCase, int, error) {
	pool := o.PoolSize
	if pool < 1 {
		pool = 1
	}
	maxKeep := o.TapeCount
	if maxKeep < 1 {
		maxKeep = 1
	}
	comp := NewCompactor(EntryPoint, o.PayloadOffset, o.StateWindow, o.resolver())
	var kept []RegressionCase
	considered := 0
	for i := 0; i < pool && len(kept) < maxKeep; i++ {
		// Spread the leading byte pseudo-randomly so most inputs land inside an
		// already-observed range and get compacted away.
		payload := append([]byte{byte((i * 37) % 256)}, []byte(fmt.Sprintf("-hdm:txn:%04d", i))...)
		var entropy [16]byte
		for j := range entropy {
			entropy[j] = byte(i + j)
		}
		rc, keep, err := comp.Consider(ctx, baseline, payload, uint64(i+1)*1_000_000_000, entropy)
		considered++
		if err != nil {
			return nil, considered, err
		}
		if keep {
			kept = append(kept, rc)
		}
	}
	return kept, considered, nil
}

// commit persists the candidate as a fresh descriptor and hot-swaps the target
// under the MVCC optimistic-concurrency check.
func (o *Orchestrator) commit(ctx context.Context, fr *FrameResult, baseRoot, targetURN string, desc *manifest.NodeDescriptor, sieve *SieveOutcome, reason string) (*FrameResult, error) {
	delete(o.escalation, targetURN) // progress: reset the cost-driven escalation counter
	newDescHash, _, err := o.repo.PutCell(targetURN, sieve.WAT, sieve.Artifact.Bytecode, desc.Semantics, desc.Saliency)
	if err != nil {
		return nil, fmt.Errorf("persist evolved descriptor: %w", err)
	}
	newRoot, cmErr := o.mvcc.ProposeEvolutionCommit(ctx, baseRoot, targetURN, newDescHash)
	if cmErr != nil {
		fr.Reason = fmt.Sprintf("commit aborted: %v", cmErr)
		return fr, nil
	}
	fr.Committed = true
	fr.NewRoot = newRoot
	fr.Reason = reason
	// Emit a lifecycle event whose kind matches the structural change, so the
	// activity visual transitions the cell correctly (live / split / fused).
	kind := "commit"
	switch fr.Kind {
	case ExecuteCellularFission:
		kind = "fission"
	case ExecuteCellularFusion:
		kind = "fusion"
	}
	o.event(kind, targetURN, reason)
	return fr, nil
}

// buildSeed renders the sieve seed for spec-driven building: the goal, the
// required entry, the acceptance checks to satisfy, and the current genotype.
// renderKnowledge retrieves the top docs + worked examples for a cell of this entry
// kind and intent from the knowledge base, formatted for the synthesis prompt. Empty
// when the stores hold nothing relevant (the static few-shot then carries synthesis).
func (o *Orchestrator) renderKnowledge(contract *EntryContract, intent string) string {
	kind := ""
	if contract == RenderFrameContract {
		kind = "render"
	}
	docs := FindDocuments(o.ledger, kind, intent, 2)
	exs := FindExamples(o.ledger, kind, intent, nil, nil, 2)
	if len(docs) == 0 && len(exs) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("RELEVANT KNOWLEDGE (retrieved for this cell — apply it):\n")
	for _, d := range docs {
		fmt.Fprintf(&b, "• %s — %s\n", d.Title, d.Body)
	}
	for _, e := range exs {
		fmt.Fprintf(&b, "WORKED EXAMPLE (%s, %s):\n%s\n", e.Kind, e.Semantics, e.WAT)
	}
	b.WriteString("\n")
	return b.String()
}

func (o *Orchestrator) buildSeed(urn, intent, genotype string, contract *EntryContract, suite *AcceptanceSuite) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Build cell %s.\nGOAL: %s\n\n", urn, intent)
	fmt.Fprintf(&b, "Export %q with signature (param i32 i32) (result i32).\n\n", contract.Name)
	ns := AppNamespaceOf(urn)
	// PLAN-FIRST: lead with the design this cell implements. The plan is the "how"
	// (its algorithm + how it connects to siblings); the acceptance checks below
	// merely VERIFY that the plan was implemented. Without this the model reinvents
	// the design every synthesis.
	if plan := LoadPlan(o.ledger, ns); plan != nil {
		if cp := plan.Component(urn); cp != nil {
			b.WriteString(cp.Render())
			b.WriteString("\n")
		}
		if sys := plan.RenderSystem(); sys != "" {
			b.WriteString(sys)
			b.WriteString("\n")
		}
	}
	// Inject the APPLICATION MAP: what the whole app is, what the sibling cells
	// already do (verified), and which shared fields are produced/consumed — so
	// this cell is built to FIT the system instead of in a vacuum (a renderer that
	// cannot see that a sibling writes player_x will just draw a static scene).
	if m := LoadAppMap(o.ledger, ns); m != nil {
		b.WriteString(m.Render())
		b.WriteString("\n")
		// Spell out THIS cell's declared interface prominently: the exact shared
		// fields it must read and write. This is its coordination contract.
		for _, c := range m.Components {
			if c.Identity != urn {
				continue
			}
			if len(c.DeclaredReads) > 0 || len(c.DeclaredWrites) > 0 {
				fmt.Fprintf(&b, "THIS CELL'S ENFORCED BOUNDARY — you may read ONLY {%s} and write ONLY {%s}. "+
					"The runtime ENFORCES this: a read of any other shared field returns 0, and a write to "+
					"any other field is discarded. Build entirely within this boundary; act on the values "+
					"you read, do not use hardcoded positions. If your logic genuinely needs another field, "+
					"the boundary itself is what must change — do not work around it.\n\n",
					strings.Join(c.DeclaredReads, ", "), strings.Join(c.DeclaredWrites, ", "))
			}
		}
	}
	// Inject the app's shared-state contract so this cell coordinates with its
	// siblings through the agreed memory layout (e.g. reads/writes player_x).
	if sc := LoadContract(o.ledger, ns); sc != nil {
		b.WriteString(sc.Render())
		b.WriteString("\n")
	}
	// RETRIEVED KNOWLEDGE: the how-to documents + worked examples from the growable
	// knowledge base most relevant to a cell of THIS kind and intent — concrete guidance
	// targeting exactly this synthesis shape (the doc explains the pattern, the example
	// shows it working). The static few-shot in the system prompt is only the floor.
	if k := o.renderKnowledge(contract, intent); k != "" {
		b.WriteString(k)
	}
	// User guidance (soft): cross-app SYSTEM principles and this APP's principles,
	// rewritten from operator commentary. The model weighs these while building; an
	// adversarial feedback critic enforces them separately.
	if s := LoadGuidance(o.ledger, SystemGuidanceKey).Render("SYSTEM GUIDANCE — principles to honor in every cell:"); s != "" {
		b.WriteString(s)
		b.WriteString("\n")
	}
	if s := LoadGuidance(o.ledger, AppGuidanceKey(ns)).Render("USER GUIDANCE for this application:"); s != "" {
		b.WriteString(s)
		b.WriteString("\n")
	}
	// Adversarial-critic feedback: if a critic judged this cell's last version against the
	// goal or the operator's criteria and REFUTED it, tell the builder exactly what was
	// wrong so it fixes that instead of re-rolling blind. Written by the visual critic (a
	// render judged by vision) or the code critic (a criterion judged against the code).
	if note := LoadCriticNote(o.ledger, urn); note != "" {
		fmt.Fprintf(&b, "CRITIC FEEDBACK on your last version (an adversarial reviewer judged it): %q\n"+
			"Change your implementation so this is fully resolved. If it is a rendering issue, the "+
			"value→color mapping is the usual culprit — use a high-contrast gradient, never a flat fill.\n\n", note)
	}
	b.WriteString("ACCEPTANCE CHECKS the cell must satisfy:\n")
	for _, c := range describeChecks(suite) {
		fmt.Fprintf(&b, "- %s: %s\n", c["name"], c["spec"])
	}
	// Advertise the reusable data-structure primitives to compute (run-tick) cells, so synthesis
	// dispatches to a shared primitive instead of re-deriving a scan/loop. UI (render-frame)
	// cells don't get it — it's noise for a draw path.
	if contract == RunTickContract {
		b.WriteString(PrimitiveVocabulary)
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "\nCURRENT GENOTYPE (improve it to pass more checks):\n%s", genotype)
	return b.String()
}

// acceptanceFrame evaluates a candidate against the cell's spec acceptance
// suite. Correctness progress (more tests passing) is committed even though it
// changes behavior; a correctness regression is rejected; an equal score falls
// back to the optimization gauntlet so a crude cell can still be trimmed.
func (o *Orchestrator) acceptanceFrame(ctx context.Context, fr *FrameResult, baseRoot, targetURN string, desc *manifest.NodeDescriptor, baseline []byte, sieve *SieveOutcome, suite *AcceptanceSuite, contract *EntryContract) (*FrameResult, error) {
	candidate := sieve.Artifact.Bytecode
	basePass, total := ScoreSuite(ctx, baseline, contract.Name, suite, o.PayloadOffset, o.StateWindow, o.resolver())
	candPass, _ := ScoreSuite(ctx, candidate, contract.Name, suite, o.PayloadOffset, o.StateWindow, o.resolver())
	fr.AcceptBase, fr.AcceptCand, fr.AcceptTotal = basePass, candPass, total

	switch {
	case candPass < basePass:
		fr.Reason = fmt.Sprintf("acceptance regression: %d->%d/%d", basePass, candPass, total)
		return fr, nil

	case candPass > basePass:
		// Correctness progress: behavior legitimately changes toward spec, so
		// the tape-reproduction gate does not apply.
		return o.commit(ctx, fr, baseRoot, targetURN, desc, sieve,
			fmt.Sprintf("correctness %d->%d/%d", basePass, candPass, total))

	default:
		// No correctness change this frame. While the product is still being
		// built (any acceptance check unmet), do NOT anneal energy: shrinking a
		// half-built cell wastes the frame and can strip scaffolding the model
		// needs to add the next behavior. Hold and keep pushing for correctness;
		// efficiency optimization is deferred until the cell passes everything.
		if basePass < total {
			// Stall: the synthesis made no acceptance headway. Count it toward
			// escalation — once past the threshold the next sieve uses the reasoner.
			o.escalation[targetURN]++
			if o.escalation[targetURN] == sieveEscalateThreshold {
				log.Printf("[MODEL] %s stalled %dx at %d/%d — escalating synthesis to the reasoner", targetURN, o.escalation[targetURN], basePass, total)
			}
			fr.Reason = fmt.Sprintf("no acceptance progress (%d/%d); deferring optimization until complete", basePass, total)
			return fr, nil
		}
		// Fully correct (basePass == total). The behavior-preserving optimization
		// gauntlet below replays run-tick regression tapes — which only applies to
		// run-tick cells. A complete render-frame (UI) cell has no such tapes, so
		// it rests here (built; the scenarios anchor its behavior).
		if contract == RenderFrameContract {
			fr.Reason = fmt.Sprintf("complete (%d/%d); UI cell rests — no run-tick optimization", candPass, total)
			return fr, nil
		}
		// Fully correct run-tick cell: a behavior-preserving optimization that
		// lowers energy is now welcome.
		cases, considered, cerr := o.compactCorpus(ctx, baseline)
		if cerr != nil {
			return nil, fmt.Errorf("compact regression corpus: %w", cerr)
		}
		fr.CorpusKept, fr.CorpusConsidered = len(cases), considered
		verdict, verr := RunGauntletCases(ctx, baseline, candidate, EntryPoint, cases,
			o.PayloadOffset, o.StateWindow, o.TokenMilliCents, o.Saliency, o.MinEnergyDrop, o.resolver())
		if verr != nil {
			fr.Reason = fmt.Sprintf("gauntlet error: %v", verr)
			return fr, nil
		}
		fr.Verdict = verdict
		if !verdict.Accepted {
			// STRUCTURAL, AMORTIZED: the per-case gauntlet runs each input in ISOLATED
			// memory, so a data structure's cost is paid every case and never recouped —
			// its win is amortized ACROSS ticks. When behavior was preserved per-case
			// (OutputMatch) but the per-case cost did not drop, replay a repeated-input
			// trajectory on SHARED memory so a cache/index built early is read cheaply
			// later. Commit if the amortized total cost drops. Structural cells only.
			if o.structural[targetURN] && verdict.OutputMatch {
				traj := trajectoryFrom(cases, structuralTrajectoryRepeats)
				if tv, tverr := RunTrajectoryGauntlet(ctx, baseline, candidate, EntryPoint, traj,
					o.TokenMilliCents, o.Saliency, o.MinEnergyDrop, o.PayloadOffset, o.StateWindow, o.resolver()); tverr == nil && tv != nil && tv.Accepted {
					fr.Verdict = tv
					if ok, reason := o.chaosOK(ctx, candidate, cases, o.resolver()); !ok {
						fr.Reason = "chaos rejected: " + reason
						return fr, nil
					}
					return o.commit(ctx, fr, baseRoot, targetURN, desc, sieve,
						fmt.Sprintf("structural refactor (acceptance %d/%d held): amortized fuel %d->%d over %d-step trajectory",
							candPass, total, tv.BaselineFuel, tv.CandidateFuel, len(traj)))
				}
			}
			// The candidate is fully correct and behavior-preserving; it simply did not lower
			// energy. That is a HOLD (an equivalent cell, no improvement), not a rejection —
			// only a behavioral divergence is a true rejection.
			if verdict.OutputMatch {
				fr.Reason = fmt.Sprintf("held (acceptance %d/%d, equivalent; no energy improvement): %s", candPass, total, verdict.Reason)
			} else {
				fr.Reason = fmt.Sprintf("rejected (acceptance %d/%d): %s", candPass, total, verdict.Reason)
			}
			return fr, nil
		}
		if ok, reason := o.chaosOK(ctx, candidate, cases, o.resolver()); !ok {
			fr.Reason = "chaos rejected: " + reason
			return fr, nil
		}
		return o.commit(ctx, fr, baseRoot, targetURN, desc, sieve,
			fmt.Sprintf("optimized (acceptance %d/%d held): fuel %d->%d", candPass, total, verdict.BaselineFuel, verdict.CandidateFuel))
	}
}

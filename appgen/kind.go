package appgen

import (
	"log"
	"strings"

	"github.com/mdcfrancis/flow/evolution"
	"github.com/mdcfrancis/flow/inference"
	"github.com/mdcfrancis/flow/storage"
)

// deriveModelType maps a cell's kind to the model type that should SYNTHESIZE it.
// Synthesis is fundamentally coding, so most kinds route to `code`; a render cell —
// whose output is visual and whose critic is the vision model — routes to `vision`
// (the omlx Gemma vision models are also capable coders), keeping a cell's builder
// and its critic on the same model.
func deriveModelType(k CellKind) inference.ModelType {
	switch k {
	case KindRender:
		return inference.ModelVision
	default: // compute, input, leaf, compose
		return inference.ModelCode
	}
}

// ModelTypeForCell resolves the synthesis model type for a live cell URN via its
// declared kind — defaults to code when the cell is not a known subsystem. Injected
// into the orchestrator so the evolution-loop sieve can route by kind without
// importing appgen (which would cycle).
func ModelTypeForCell(ledger *storage.LedgerEngine, cellURN string) inference.ModelType {
	k := CellKind(CellKindOf(ledger, cellURN))
	if !k.valid() {
		return inference.ModelCode
	}
	return deriveModelType(k)
}

// CellKind is a cell's declared TYPE — a fixed, extensible ontology that binds
// together, in one authoritative fact, the four things that used to be inferred
// independently (and could disagree): the entry export, the genesis contract, the
// legal scenario shape, and the I/O-port invariant. Declared by the envelope/
// fracture author and validated against the declared ports; ports win on any
// contradiction (a cell that writes shared state can never be a renderer).
//
// The set is closed today so every `switch kind` stays total; adding a kind is a
// single registry entry, not edits scattered across the pipeline.
type CellKind string

const (
	KindCompute CellKind = "compute" // run-tick: reads/writes shared state, emits no draw
	KindRender  CellKind = "render"  // render-frame: reads state, emits a draw stream, writes no state
	KindInput   CellKind = "input"   // run-tick: reads the HMI register, writes shared state
	KindLeaf    CellKind = "leaf"    // run-tick: pure per-element fn at the arg pointer, no contract fields
	KindCompose CellKind = "compose" // generated combinator driver (Composition != nil); no acceptance suite
)

// kindSpec is the per-kind policy the pipeline reads instead of re-deriving type.
type kindSpec struct {
	Entry       string // the exported entry: "run-tick" | "render-frame"
	EmitsDraw   bool   // produces a draw stream (a render cell)
	WritesState bool   // mutates shared contract fields
	ReadsHMI    bool   // consumes the HMI input register
	IsUI        bool   // shorthand: graded/authored as a UI/render cell
}

// cellKinds is the ontology registry. EXTENSIBLE: a new kind is one entry here.
var cellKinds = map[CellKind]kindSpec{
	KindCompute: {Entry: "run-tick", WritesState: true},
	KindRender:  {Entry: "render-frame", EmitsDraw: true, IsUI: true},
	KindInput:   {Entry: "run-tick", WritesState: true, ReadsHMI: true},
	KindLeaf:    {Entry: "run-tick"},
	KindCompose: {Entry: "run-tick"},
}

// spec returns a kind's policy, defaulting to compute for an unknown/empty kind
// so callers never panic on a legacy value.
func (k CellKind) spec() kindSpec {
	if s, ok := cellKinds[k]; ok {
		return s
	}
	return cellKinds[KindCompute]
}

// valid reports whether k is a known kind.
func (k CellKind) valid() bool { _, ok := cellKinds[k]; return ok }

// hasHMIRead reports whether a reads list names the HMI input register.
func hasHMIRead(reads []string) bool {
	for _, r := range reads {
		if strings.Contains(strings.ToLower(r), "hmi") {
			return true
		}
	}
	return false
}

// nonHMIReads counts the shared CONTRACT fields a subsystem reads (excluding the
// "HMI input" register, which is not a contract field).
func nonHMIReads(reads []string) int {
	n := 0
	for _, r := range reads {
		if !strings.Contains(strings.ToLower(r), "hmi") {
			n++
		}
	}
	return n
}

// deriveKind infers a subsystem's kind from its declared I/O ports — the ground
// truth the architecture is built on (writers write, the view only draws). A
// WRITER of shared state is classified by ports alone, so a physics cell whose
// semantics happen to mention "colors" or "scene" can never be mistaken for a
// renderer. Only when the ports carry NO signal (empty) does it fall back to the
// keyword heuristic — and even then a writer is unreachable, so the physics
// mistyping stays impossible. Leaf-ness is a GRAPH fact resolved in
// normalizeKinds (a leaf is a composition's leaf function), not here.
func deriveKind(sub Subsystem) CellKind {
	if sub.Composition != nil {
		return KindCompose
	}
	if len(sub.Writes) > 0 { // writes shared state
		if hasHMIRead(sub.Reads) {
			return KindInput
		}
		return KindCompute
	}
	// An INITIALIZER seeds shared state — it is a compute cell that writes, never a view
	// that draws. A mis-designed init that declared reads-only (or nothing) must still be
	// run-tick, not render-frame, so it initializes state instead of drawing a frame
	// buffer. (A real writer is already compute above; this only rescues an
	// under-declared init before the reads-only branch turns it into a renderer.)
	if isInitSubsystem(sub.Semantics) && !hasHMIRead(sub.Reads) {
		return KindCompute
	}
	if nonHMIReads(sub.Reads) > 0 { // reads state, writes none ⇒ a live view that draws it
		return KindRender
	}
	// Empty ports: a composition leaf, a plain scalar compute cell, or a render cell
	// that under-declared its reads. Break the tie with the semantics keyword — never
	// reachable for a state-writer, so it cannot reintroduce the render/compute bug.
	if isUISubsystem(sub.Semantics) {
		return KindRender
	}
	return KindCompute
}

// reconcileKind returns the kind to use for a subsystem, honoring the DECLARED
// kind only when it is consistent with the port facts, and otherwise OVERRIDING
// it with the port-derived kind. Ports win: this is what makes the physics
// mistyping structurally impossible. `overridden` is true when the declaration
// was rejected (for logging).
func reconcileKind(sub Subsystem) (kind CellKind, overridden bool) {
	derived := deriveKind(sub)
	decl := CellKind(strings.ToLower(strings.TrimSpace(string(sub.Kind))))
	if !decl.valid() {
		return derived, false // nothing usable declared — derive silently
	}
	// A composition subsystem is defined by its Composition object, not a label.
	if sub.Composition != nil {
		return KindCompose, decl != KindCompose
	}
	// Hard port contradictions the declaration cannot override:
	//  - declared render but writes state  → it's a writer, not a view.
	//  - declared render but reads HMI      → input-driven, not a view.
	//  - declared non-render but the ports say pure view (writes nothing, reads state)
	//    is allowed to stand as compute ONLY if it truly writes nothing AND the
	//    declaration is render/leaf; a writer declared render is the dangerous case.
	//  - declared render but its role is INITIALIZATION → it seeds state, never draws.
	if decl == KindRender && (len(sub.Writes) > 0 || hasHMIRead(sub.Reads) || isInitSubsystem(sub.Semantics)) {
		return derived, true
	}
	// declared leaf is honored only when the ports are truly empty (a leaf works on
	// the arg pointer, not shared fields); otherwise it touches state → not a leaf.
	if decl == KindLeaf {
		if len(sub.Writes) == 0 && nonHMIReads(sub.Reads) == 0 && !hasHMIRead(sub.Reads) {
			return KindLeaf, false
		}
		return derived, true
	}
	// declared input but reads no HMI / writes nothing → not an input cell.
	if decl == KindInput && (!hasHMIRead(sub.Reads) || len(sub.Writes) == 0) {
		return derived, true
	}
	// declared compute but writes nothing while reading state → it's a view.
	if decl == KindCompute && len(sub.Writes) == 0 && derived == KindRender {
		return derived, true
	}
	return decl, false
}

// normalizeKinds fills and repairs the Kind on every subsystem of an envelope,
// in place, from the declaration reconciled against the ports. Idempotent, so it
// is safe to call at author time (to persist) AND at load time (so legacy
// envelopes with no declared kinds derive them for free — no migration write).
// Logs any override so a bad declaration is visible.
func normalizeKinds(env *AppEnvelope) {
	if env == nil {
		return
	}
	for i := range env.SubsystemRequirements {
		sub := env.SubsystemRequirements[i]
		kind, overridden := reconcileKind(sub)
		// A composition's leaf function is authoritatively a leaf (a graph fact that
		// ports/semantics can't express), regardless of what was declared or derived.
		if isCompositionLeaf(env, sub.Identity) {
			kind, overridden = KindLeaf, false
		}
		if overridden {
			log.Printf("[KIND] %s declared %q but ports say %q — using %q (ports win)",
				sub.Identity, sub.Kind, kind, kind)
		}
		env.SubsystemRequirements[i].Kind = kind
	}
}

// kindOf returns a subsystem's kind, reconciling on the fly if it was not
// normalized (defensive — callers should rely on normalizeKinds having run).
func kindOf(sub Subsystem) CellKind {
	// reconcileKind is the authority: it honors a valid declared kind ONLY when the
	// ports agree, and corrects a hard contradiction (e.g. a cell DECLARED render that
	// writes shared state — a writer, not a view — which otherwise gets a render-frame
	// contract and is told to draw instead of initializing state).
	k, _ := reconcileKind(sub)
	return k
}

// entryFor returns the exported entry ("run-tick"/"render-frame") for a subsystem.
func entryFor(sub Subsystem) string { return kindOf(sub).spec().Entry }

// IsRender reports whether a subsystem is a RENDER cell, from its declared kind
// (validated against ports). Prefer this over IsUISubsystem when the Subsystem is
// in hand — it is authoritative, not a keyword guess.
func (s Subsystem) IsRender() bool { return kindOf(s) == KindRender }

// CellKindOf returns the declared kind (as a string) for a live cell URN, for
// inspectors/observability. Empty when the URN is not an app subsystem.
func CellKindOf(ledger *storage.LedgerEngine, cellURN string) string {
	env := LoadEnvelope(ledger, evolution.AppNamespaceOf(cellURN))
	if env == nil {
		return ""
	}
	for _, s := range env.SubsystemRequirements {
		if s.Identity == cellURN {
			return string(kindOf(s))
		}
	}
	return ""
}

// kindForCell resolves a cell URN's declared kind via its app envelope, for sites
// that hold only a URN/semantics string (not the Subsystem). Falls back to
// KindCompute when the cell is not a known subsystem — a safe, non-UI default that
// never grades a cell through the render entry by mistake.
func kindForCell(ledger *storage.LedgerEngine, cellURN string) CellKind {
	env := LoadEnvelope(ledger, evolution.AppNamespaceOf(cellURN))
	if env == nil {
		return KindCompute
	}
	for _, s := range env.SubsystemRequirements {
		if s.Identity == cellURN {
			return kindOf(s)
		}
	}
	return KindCompute
}

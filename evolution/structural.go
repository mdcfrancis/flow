package evolution

import (
	"context"
	"fmt"
	"strings"

	"github.com/mdcfrancis/flow/compiler"
	"github.com/mdcfrancis/flow/manifest"
)

// StructureToolkit is the data-structure action space offered to the model when a cell's
// LOCAL optimization has plateaued but it is still expensive. Cost is the trigger; a data
// structure is the remedy. The model diagnoses the cost SHAPE and either reuses one of the
// shared private-page primitives (via cell-dispatch) or mints a new one — turning "introduce
// a data structure" into a legal optimizer move. The verification gates are unchanged: the
// refactor is replayed against the cell's regression tapes / acceptance, so only a
// behavior-preserving change that lowers cost is committed.
const StructureToolkit = `
STRUCTURAL OPTIMIZATION — this cell is EXPENSIVE and local rewrites have plateaued. You may now
REDUCE ITS COST by offloading work to a shared DATA-STRUCTURE primitive instead of doing it
inline. Behavior MUST stay identical: your candidate is replayed against regression tapes, and
only a behavior-preserving change that lowers fuel/latency is accepted.

DIAGNOSE the cost shape, then reach for the matching structure:
  - recomputing the same value across ticks    -> CACHE it in a private page (see pattern below)
  - linear scan / repeated search over data   -> associative map   (sys:dict)
  - membership tests                           -> set              (sys:set)
  - append + indexed read                      -> dynamic array    (sys:list)
  - element-wise transform / reduce over array -> map/fold/filter/scan/zip/iterate (sys:*)

CACHE PATTERN (the simplest and highest-value: skip the expensive work when the input recurs).
Keep the last (key,result) in your private window (base 0x00400000); on a repeat, return it
without recomputing. Preserve behavior exactly — the output must be identical, only cheaper:

  (local.set $key (i32.load (local.get $arg)))          ;; the input that determines the result
  (if (i32.eq (i32.load (i32.const 0x00400000)) (i32.add (local.get $key) (i32.const 1)))
    (then (return (i32.load (i32.const 0x00400004)))))  ;; cache HIT: return stored result
  ;; ... cache MISS: compute the result the same way the current cell does ...
  (i32.store (i32.const 0x00400000) (i32.add (local.get $key) (i32.const 1)))  ;; store key+1 (0=empty)
  (i32.store (i32.const 0x00400004) (local.get $result))
  (return (local.get $result))

PRIVATE-PAGE ABI (host imports; a primitive's state lives in a private page you own):
  (import "hdm:kernel/paging" "alloc"  (func $palloc  (param i32) (result i32)))  ;; alloc(size)->handle
  (import "hdm:kernel/paging" "select" (func $pselect (param i32) (result i32)))  ;; select(handle): the
       ;; NEXT dispatched op operates on that instance's page. Call before each op.
  (import "hdm:kernel/cell-dispatch" "invoke-cell" (func $invoke (param i32 i32 i32 i32) (result i32)))
       ;; invoke-cell(urnPtr,urnLen,argPtr,argLen)->i32. Write the primitive's URN bytes and the op
       ;; args into memory, pass their pointers. Persist your page HANDLE across ticks in your own
       ;; shared-state field so the same instance is reused.

PRIMITIVE OP ABIs (arg buffer = little-endian i32 words at argPtr):
  sys:dict  [op,key,val]  op 0=GET -> value (0 if absent); op 1=INSERT -> 1. keys >= 1.
  sys:set   [op,key]      op 0=CONTAINS -> 0/1; op 1=ADD -> 1. keys >= 1.
  sys:list  [op,x]        op 0=LEN -> length; op 1=APPEND(x) -> new length; op 2=GET(idx=x) -> element.
  sys:map/fold/filter/scan/zip/iterate: apply a passed-in leaf cell across an array; config layouts
       are documented with each combinator.

OUTPUT ORDER: emit your refactored version of THIS cell FIRST, as the primary (module ...) block.

If NONE of the existing primitives fit the cost shape, you MAY also mint a NEW primitive: AFTER
the refactored cell, append a section marked exactly

  NEW-PRIMITIVE urn:hdm:sys:<name>
  ` + "```" + `wat
  (module (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 100))
    (func (export "run-tick") (param i32 i32) (result i32) ... ))
  ` + "```" + `

The new primitive is pure ops over its own private window (base 0x00400000), opcode-in-args like
the others; your refactored cell dispatches to its URN. It is trusted ONLY because this cell,
using it, still reproduces its tapes — so make it correct.
`

// PrimitiveVocabulary is the CONCISE list of reusable data-structure primitives advertised to
// EVERY run-tick cell build, so app synthesis reaches for a shared primitive (via cell-dispatch)
// instead of hand-writing a loop — the token-efficiency payoff of the private-page library. The
// verbose StructureToolkit (with the cache/mint patterns) is reserved for structural escalation.
const PrimitiveVocabulary = `
REUSABLE PRIMITIVES — prefer dispatching to these over hand-writing a scan/loop. Call
hdm:kernel/cell-dispatch invoke-cell(urnPtr,urnLen,argPtr,argLen): write the primitive's URN
bytes and the op-args (little-endian i32) into memory and pass their pointers.
  sys:dict [op,key,val]  op0=GET->value(0 if absent)  op1=INSERT->1     (uint32->uint32 map; keys>=1)
  sys:set  [op,key]      op0=CONTAINS->0/1  op1=ADD->1
  sys:list [op,x]        op0=LEN  op1=APPEND(x)->len  op2=GET(idx=x)
  sys:map/fold/filter/scan/zip/iterate — apply a passed-in leaf cell across an array.
A primitive's state lives in a PRIVATE PAGE you own: hdm:kernel/paging alloc(size)->handle creates
one (you become its owner; persist the handle in a shared field), select(handle) makes it the page
the next dispatched op operates on. The page is invisible to other cells.`

// renderStructural returns the toolkit block to append to a synthesis seed when a cell is in
// structural mode. Kept a function so the trigger/formatting can evolve without touching callers.
func renderStructural() string { return StructureToolkit }

// structuralSeed frames a structural refactor as an OPTIMIZATION of a WORKING cell, not a
// rebuild: it leads with the current genotype and "preserve exact behavior", lists the checks
// the refactor must still pass, then offers the data-structure toolkit. This ordering matters —
// under the plain build prompt the model rewrites from scratch and loses the behavior.
func (o *Orchestrator) structuralSeed(urn string, contract *EntryContract, genotype string, suite *AcceptanceSuite) string {
	var b strings.Builder
	fmt.Fprintf(&b, "OPTIMIZE the working cell %s. It ALREADY works and passes every check below. "+
		"Make it CHEAPER (fewer function calls / less recomputation) by using a DATA STRUCTURE, while "+
		"producing the EXACT SAME output for every input. REFACTOR the genotype below — do NOT rewrite "+
		"from scratch and do NOT change what it computes. Export %q with signature (param i32 i32) (result i32).\n\n",
		urn, contract.Name)
	b.WriteString("CURRENT GENOTYPE — this is CORRECT; preserve its behavior exactly:\n")
	b.WriteString(genotype)
	b.WriteString("\n\nCHECKS your refactor must STILL pass (same input => same output):\n")
	for _, c := range describeChecks(suite) {
		fmt.Fprintf(&b, "- %s: %s\n", c["name"], c["spec"])
	}
	b.WriteString("\n")
	b.WriteString(renderStructural())
	b.WriteString("\nOutput ONE complete (module ...): your refactored, cheaper, behavior-identical cell.")
	return b.String()
}

// extractNewPrimitive finds a "NEW-PRIMITIVE <urn>" marker in a model response and returns that
// URN plus the FIRST (module ...) form appearing after the marker. Returns found=false if no
// well-formed mint section is present. The URN must be a sys:* URN (a minted primitive).
func extractNewPrimitive(raw string) (urn, wat string, found bool) {
	i := strings.Index(raw, "NEW-PRIMITIVE")
	if i < 0 {
		return "", "", false
	}
	rest := raw[i+len("NEW-PRIMITIVE"):]
	// URN is the first whitespace-delimited token beginning urn:hdm:sys: on that line.
	line := rest
	if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
		line = rest[:nl]
	}
	for _, tok := range strings.Fields(line) {
		if strings.HasPrefix(tok, "urn:hdm:sys:") {
			urn = tok
			break
		}
	}
	if urn == "" {
		return "", "", false
	}
	// The primitive module is the first (module ...) after the marker.
	mods := extractAllWAT(rest)
	if len(mods) == 0 {
		return "", "", false
	}
	return urn, mods[0], true
}

// provisionMint stores a candidate primitive so o.resolver() can resolve it while the refactored
// consumer is verified. It is provisional: kept only if the consumer commits (witnessed), else
// retireMint removes it. Refuses to store over an existing live cell (never clobber a primitive).
// Returns true if a fresh provisional primitive was stored.
func (o *Orchestrator) provisionMint(urn, wat string) bool {
	if _, err := o.repo.Load(urn); err == nil {
		return false // already exists — treat as reuse, not a mint
	}
	art, err := compiler.NewCompilerService().CompileGenotype(wat)
	if err != nil || art == nil || !art.SyntaxPassed {
		return false
	}
	if checkEntrySignature(context.Background(), art.Bytecode, RunTickContract) != nil {
		return false // must be a run-tick primitive
	}
	sem := manifest.SemanticManifest{
		FunctionalIntent: "minted data-structure primitive (witnessed by a consuming cell)",
		DomainTags:       []string{"sys", "primitive", "minted"},
	}
	descHash, _, err := o.repo.PutCell(urn, wat, art.Bytecode, sem, 0)
	if err != nil {
		return false
	}
	if o.repo.SeedRef(urn, descHash) != nil {
		return false
	}
	return true
}

// retireMint removes a provisional primitive whose consumer did NOT commit — it was never
// witnessed correct, so it must not linger as a resolvable (untrusted) cell.
func (o *Orchestrator) retireMint(urn string) {
	if o.ledger != nil {
		_ = o.ledger.DeleteRefs(urn)
	}
}

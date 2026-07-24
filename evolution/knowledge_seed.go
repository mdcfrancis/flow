package evolution

import (
	"log"

	"github.com/mdcfrancis/flow/compiler"
	"github.com/mdcfrancis/flow/storage"
)

// seedDrawAtPositionWAT is a WORKED render cell: it reads ball_x (0xB0000) and ball_y
// (0xB0004) from the shared contract and emits ONE circle record at those coordinates —
// the exact "read a field, draw there" pattern the single-ball renderer kept missing. A
// circle record is op=3 with a=cx, b=cy, c=radius; the 24-byte stream is [op,a,b,c,d,rgba].
const seedDrawAtPositionWAT = `(module
  (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 100))
  (func (export "render-frame") (param $base i32) (param $cap i32) (result i32)
    local.get $base i32.const 3 i32.store
    local.get $base i32.const 4 i32.add i32.const 0xB0000 i32.load i32.store
    local.get $base i32.const 8 i32.add i32.const 0xB0004 i32.load i32.store
    local.get $base i32.const 12 i32.add i32.const 8 i32.store
    local.get $base i32.const 16 i32.add i32.const 0 i32.store
    local.get $base i32.const 20 i32.add i32.const 0xFFCC33FF i32.store
    i32.const 24))`

// seedDocs is the starter DOCUMENT set — the ABI/pattern knowledge that today lives only
// inside evolution.Capabilities and the prompts, lifted into the retrievable store.
var seedDocs = []Document{
	// System-level DESIGN docs — no Kinds, so they are relevant to every retrieval
	// and, in particular, surface for the whole-app design pass (AuthorPlan), which
	// reasons across components rather than about one entry. These describe HOW to
	// decompose and connect an app, not how to write one cell.
	{Topic: "tick-loop-architecture", Title: "Decompose a real-time app into a tick loop of single-job cells",
		Provenance: "seed",
		Body: "A live app is a set of cells that all run every tick/frame and coordinate ONLY through " +
			"shared-state fields — never by calling each other. The canonical decomposition: an INPUT cell " +
			"reads the HMI register and writes control fields (e.g. player_x); one or more COMPUTE cells read " +
			"state, advance the simulation (positions, velocities, collisions, score) and write it back; a " +
			"RENDER cell reads the state and draws every entity at the value it reads. Give each cell ONE entry " +
			"and ONE job — if a cell would both simulate and draw, split it into a compute cell and a render cell."},
	{Topic: "coordinate-through-state", Title: "Connect components as an ordered sequence of shared-field handoffs",
		Provenance: "seed",
		Body: "Design the choreography as: which field is written first each tick, and which cell reads it next. " +
			"A writer stores a field at an offset; its reader loads the SAME offset. State written this tick is " +
			"visible next tick, so order the sequence input → physics → render. Name every handoff explicitly " +
			"(input writes player_x; physics reads player_x and the ball, updates ball_x/ball_y; render reads all " +
			"of them and draws). A field with a writer but no reader, or a reader with no writer, is a design bug."},
	{Topic: "simulate-then-view", Title: "Separate the model (state) from the view (drawing)",
		Provenance: "seed",
		Body: "Keep the simulation and the rendering in different cells. The simulation owns the truth: it " +
			"integrates positions and velocities, resolves collisions, and updates score/lives, writing all of " +
			"it to shared state. The view is a pure function of that state — each frame it reads the fields and " +
			"draws, holding no state of its own and moving nothing. This makes motion verifiable: the physics " +
			"cell is checked on the state trajectory, the view on drawing AT the state it reads."},
	{Topic: "draw-at-position", Title: "Read a contract field and draw at it",
		Kinds:      []string{"render"},
		Provenance: "seed",
		Body: "A live view must DRAW AT THE STATE, not at a fixed spot. Read the entity's " +
			"coordinates from their shared-memory offsets (e.g. ball_x at 0xB0000, ball_y at " +
			"0xB0004, i32 each), then write ONE draw record whose position fields are those " +
			"loaded values — never constants. A circle record is op=3 with a=cx, b=cy, c=radius. " +
			"If you store constants into the position fields the sprite is drawn at a fixed place " +
			"and the position-coordination checks fail."},
	{Topic: "wall-bounce", Title: "Integrate a position and reflect it at a boundary",
		Kinds:      []string{"compute"},
		Provenance: "seed",
		Body: "Each tick: pos += vel. Then bounce: if pos - radius <= 0 OR pos + radius >= bound, " +
			"negate vel (vel = 0 - vel) AND clamp pos back inside [radius, bound-radius] so it " +
			"cannot stick past the wall. Do this independently for x (against screen_width) and y " +
			"(against screen_height). Write the updated pos and vel back to their contract offsets."},
	{Topic: "draw-stream", Title: "The canvas vector draw-stream format",
		Kinds:      []string{"render"},
		Provenance: "seed",
		Body: "render-frame(base,cap) writes a sequence of fixed 24-byte little-endian records at " +
			"base and RETURNS the total byte length. Each record is [op i32, a i32, b i32, c i32, " +
			"d i32, rgba i32]. op low byte = primitive: 1 rect(x=a,y=b,w=c,h=d), 2 line(a,b,c,d), " +
			"3 circle(cx=a,cy=b,r=c). op high byte = layer (0 basemap,1 app). rgba is 0xRRGGBBAA. " +
			"Canvas is 320x240."},
	{Topic: "memory-map", Title: "Shared-memory map",
		Provenance: "seed",
		Body: "Offsets into shared-cluster-memory: 0x00000 system registry (read-only); 0x10000 " +
			"inbound packet frame; 0x50000 HMI input register (mouse/keyboard, read-only); 0x51000 " +
			"canvas draw-output; 0xB0000+ dynamic sandbox — shared app state that PERSISTS across " +
			"ticks. Application contract fields live in the sandbox; a writer and its reader must " +
			"use the SAME offset."},
	{Topic: "wat-abi", Title: "WAT ABI + assembler constraints",
		Provenance: "seed",
		Body: "Import the shared memory as (import \"hdm:kernel/hardware-io\" \"shared-cluster-memory\" " +
			"(memory 100)). Export exactly ONE entry: run-tick(i32,i32)->i32 (compute) or " +
			"render-frame(i32,i32)->i32 (UI). The hand-written assembler is strict: at most ONE " +
			"table per module; keep types consistent (i32 throughout — do not mix i64 into an i32 " +
			"op like i32.div_s); put all (local ...) at the top of the function; balance parens."},
}

// SeedKnowledge idempotently seeds the starter documents and any compilable seed
// examples into the ledger (AddDocument is topic-keyed, AddExample novelty-gated, so
// re-seeding is a no-op). A seed example is COMPILE-CHECKED before it is stored, so a
// bad hand-written WAT is silently skipped rather than poisoning retrieval.
func SeedKnowledge(ledger *storage.LedgerEngine) {
	if ledger == nil {
		return
	}
	for _, d := range seedDocs {
		_ = AddDocument(ledger, d)
	}
	svc := compiler.NewCompilerService()
	seedExamples := []Example{
		{Kind: "render", Entry: "render-frame", Semantics: "read ball_x and ball_y and draw a filled circle at that position",
			Reads: []string{"ball_x", "ball_y"}, Tags: []string{"draw-at-position"},
			WAT: seedDrawAtPositionWAT, Score: "seed", Provenance: "seed"},
	}
	kept := 0
	for _, e := range seedExamples {
		if art, err := svc.CompileGenotype(e.WAT); err != nil || art == nil || !art.SyntaxPassed {
			log.Printf("[DOC] seed example %q skipped (did not compile)", e.Semantics)
			continue
		}
		if ok, _ := AddExample(ledger, e); ok {
			kept++
		}
	}
	log.Printf("[DOC] knowledge base seeded: %d documents, %d example(s)", len(seedDocs), kept)
}

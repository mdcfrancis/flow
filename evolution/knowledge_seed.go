package evolution

import (
	"log"

	"github.com/mdcfrancis/flow/flux"
	"github.com/mdcfrancis/flow/storage"
)

// The seed EXAMPLES are worked FLUX cells — Flux is the language the model authors in,
// so its worked examples are Flux too (not WAT). seedDrawAtPositionFlux is the "read a
// field, draw there" render pattern the single-ball renderer kept missing;
// seedWallBounceFlux is the compute pattern: integrate, reflect at the walls, clamp.
const seedDrawAtPositionFlux = `(cell renderer (reads ball_x ball_y) (draw (circle ball_x ball_y 8 #xFFCC33FF)))`

const seedWallBounceFlux = `(cell physics
  (reads ball_x ball_y vel_x vel_y screen_w screen_h)
  (writes ball_x ball_y vel_x vel_y)
  (let ([nx (+ ball_x vel_x)] [ny (+ ball_y vel_y)]
        [bx (or (< nx 0) (>= nx screen_w))] [by (or (< ny 0) (>= ny screen_h))])
    (write (vel_x (if bx (neg vel_x) vel_x)) (vel_y (if by (neg vel_y) vel_y))
           (ball_x (clamp nx 0 (- screen_w 1))) (ball_y (clamp ny 0 (- screen_h 1))))))`

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
	{Topic: "flux-authoring", Title: "You write Flux; the lowerer owns the ABI",
		Provenance: "seed",
		Body: "Author cells in FLUX — a typed (cell NAME …) S-expression, never WAT or (module …). " +
			"A cell is a PURE FUNCTION over shared state: (reads …)/(writes …) with a body ending in " +
			"(write (field expr) …) for compute, or (reads …)(draw …) for a view. You write ONLY logic. " +
			"The lowerer owns everything else — the module wrapper, the memory import, the entry export " +
			"(run-tick for compute, render-frame for a view), locals, field addressing, the draw-record " +
			"ABI, and all stack/type discipline. Types are Int/Float/Bool/Color and never implicitly " +
			"coerce; comparisons yield Bool. Declare operator knobs as capabilities: (requires (scalar NAME MIN MAX))."},
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
	seedExamples := []Example{
		{Kind: "render", Entry: "render-frame", Semantics: "read ball_x and ball_y and draw a filled circle at that position",
			Reads: []string{"ball_x", "ball_y"}, Tags: []string{"draw-at-position"},
			Genotype: seedDrawAtPositionFlux, Score: "seed", Provenance: "seed"},
		{Kind: "compute", Entry: "run-tick", Semantics: "integrate a position and reflect it at the walls, clamping inside",
			Reads: []string{"ball_x", "ball_y", "vel_x", "vel_y", "screen_w", "screen_h"},
			Writes: []string{"ball_x", "ball_y", "vel_x", "vel_y"}, Tags: []string{"wall-bounce"},
			Genotype: seedWallBounceFlux, Score: "seed", Provenance: "seed"},
	}
	// The seed genomes are FLUX, so validate them through the Flux front end (parse +
	// type-check + lower) against a small layout covering the fields they reference.
	seedLayout := flux.Layout{
		"ball_x": {Type: flux.TInt, Offset: 0xB0000}, "ball_y": {Type: flux.TInt, Offset: 0xB0004},
		"vel_x": {Type: flux.TInt, Offset: 0xB0008}, "vel_y": {Type: flux.TInt, Offset: 0xB000C},
		"screen_w": {Type: flux.TInt, Offset: 0xB0010}, "screen_h": {Type: flux.TInt, Offset: 0xB0014},
	}
	kept := 0
	for _, e := range seedExamples {
		if _, err := flux.Compile("seed", e.Genotype, seedLayout); err != nil {
			log.Printf("[DOC] seed example %q skipped (did not compile: %v)", e.Semantics, err)
			continue
		}
		if ok, _ := AddExample(ledger, e); ok {
			kept++
		}
	}
	log.Printf("[DOC] knowledge base seeded: %d documents, %d example(s)", len(seedDocs), kept)
}

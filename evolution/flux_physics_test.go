package evolution

import (
	"context"
	"testing"

	"github.com/mdcfrancis/flow/compiler"
	"github.com/mdcfrancis/flow/flux"
)

// The physics cell that failed 31× as raw WAT (see docs/functional-ir.md),
// authored in Flux. This test lowers it through flux.Compile and runs the result
// through the SAME operational scenario-scoring harness the live grow uses for
// acceptance — proving a Flux-authored cell behaves correctly end to end, not
// just that it assembles.
const fluxPhysics = `
(cell physics
  (reads  ball_x ball_y vel_x vel_y screen_w screen_h)
  (writes ball_x ball_y vel_x vel_y)
  (let ([nx (+ ball_x vel_x)]
        [ny (+ ball_y vel_y)]
        [bx (or (< nx 0) (>= nx screen_w))]
        [by (or (< ny 0) (>= ny screen_h))])
    (write
      (vel_x (if bx (neg vel_x) vel_x))
      (vel_y (if by (neg vel_y) vel_y))
      (ball_x (clamp nx 0 (- screen_w 1)))
      (ball_y (clamp ny 0 (- screen_h 1))))))
`

func fluxBallLayout() flux.Layout {
	return flux.Layout{
		"ball_x":   {Type: flux.TInt, Offset: 0xB0000},
		"ball_y":   {Type: flux.TInt, Offset: 0xB0004},
		"vel_x":    {Type: flux.TInt, Offset: 0xB0008},
		"vel_y":    {Type: flux.TInt, Offset: 0xB000C},
		"screen_w": {Type: flux.TInt, Offset: 0xB0010},
		"screen_h": {Type: flux.TInt, Offset: 0xB0014},
	}
}

func compileFluxCell(t *testing.T, src string, layout flux.Layout) []byte {
	t.Helper()
	wat, err := flux.Compile("cell", src, layout)
	if err != nil {
		t.Fatalf("flux compile: %v", err)
	}
	art, err := compiler.NewCompilerService().CompileGenotype(wat)
	if err != nil || art == nil || !art.SyntaxPassed {
		t.Fatalf("assemble lowered WAT: %v\n---- lowered ----\n%s", err, wat)
	}
	return art.Bytecode
}

func fluxI32(v int32) *int32 { return &v }

func TestFluxPhysicsMovesAndBouncesOperationally(t *testing.T) {
	bc := compileFluxCell(t, fluxPhysics, fluxBallLayout())

	scen := []Scenario{
		{Name: "moves_by_velocity", Entry: "run-tick", Steps: 1,
			Seed: []SeedWrite{
				{At: "0xB0000", U32: []uint32{100}}, // ball_x
				{At: "0xB0008", U32: []uint32{5}},   // vel_x
				{At: "0xB0010", U32: []uint32{320}}, // screen_w
				{At: "0xB0014", U32: []uint32{240}}, // screen_h
			},
			Expect: ScenarioExpect{Reads: []SeedWrite{{At: "0xB0000", U32: []uint32{105}}}}},

		{Name: "reflects_at_right_wall", Entry: "run-tick", Steps: 1,
			Seed: []SeedWrite{
				{At: "0xB0000", U32: []uint32{318}},
				{At: "0xB0008", U32: []uint32{5}},
				{At: "0xB0010", U32: []uint32{320}},
				{At: "0xB0014", U32: []uint32{240}},
			},
			// vel_x flips 5 -> -5 (decreased) and ball_x is clamped inside the wall.
			Expect: ScenarioExpect{Reads: []SeedWrite{
				{At: "0xB0008", Cmp: "decreased", U32: []uint32{5}},
				{At: "0xB0000", Cmp: "le", U32: []uint32{319}},
			}}},

		{Name: "stays_in_bounds_over_time", Entry: "run-tick", Steps: 40,
			Seed: []SeedWrite{
				{At: "0xB0000", U32: []uint32{300}},
				{At: "0xB0008", U32: []uint32{7}},
				{At: "0xB0010", U32: []uint32{320}},
				{At: "0xB0014", U32: []uint32{240}},
			},
			Expect: ScenarioExpect{Trajectory: []TrajectoryExpect{
				{At: "0xB0000", InBoundsMin: fluxI32(0), InBoundsMax: fluxI32(319), MinDistinct: 5},
			}}},
	}

	pass, total := ScenarioScore(context.Background(), bc, scen, DefaultPayloadOffset, DefaultStateWindow, nil)
	if pass != total {
		t.Fatalf("Flux physics passed only %d/%d operational scenarios", pass, total)
	}
}

// The view path: a Flux renderer that draws the ball AT its read position must
// track shared state — the same position-tracking check (drawpos) the live grow
// applies to render cells.
const fluxRenderer = `
(cell renderer
  (reads ball_x ball_y)
  (draw
    (circle ball_x ball_y 8 #xFFCC33FF)))
`

func TestFluxRendererTracksPositionOperationally(t *testing.T) {
	layout := flux.Layout{
		"ball_x": {Type: flux.TInt, Offset: 0xB0000},
		"ball_y": {Type: flux.TInt, Offset: 0xB0004},
	}
	bc := compileFluxCell(t, fluxRenderer, layout)

	x := 250
	scen := []Scenario{{Name: "draws_at_ball_x", Entry: "render-frame",
		Seed:   []SeedWrite{{At: "0xB0000", U32: []uint32{250}}},
		Expect: ScenarioExpect{Draw: &DrawExpect{Op: "circle", MinRecords: 1, NearX: &x}}}}

	if pass, total := ScenarioScore(context.Background(), bc, scen, DefaultPayloadOffset, DefaultStateWindow, nil); pass != total {
		t.Fatalf("Flux renderer passed only %d/%d operational scenarios", pass, total)
	}
}

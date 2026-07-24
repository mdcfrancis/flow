package appgen

import (
	"testing"

	"github.com/mdcfrancis/flow/evolution"
)

// A renderer writes no state, so a directional (state-change) reads-postcondition
// on a render-frame scenario is un-satisfiable and must be dropped — a correct
// renderer must never be failed by a check for physics behavior it doesn't do.
func TestGroundScenariosDropsStateChangeOnRenderer(t *testing.T) {
	c := &evolution.AppContract{Fields: []evolution.ContractField{
		{Name: "ball_x", Offset: 0xB0000, Type: "i32", Init: 40},
		{Name: "ball_vx", Offset: 0xB0008, Type: "i32", Init: 5},
	}}
	nx := 40
	scs := []evolution.Scenario{
		// A render scenario that (wrongly) asserts ball_vx DECREASED plus a valid draw
		// at the seeded ball position.
		{Name: "render-after-bounce", Entry: "render-frame",
			Seed: []evolution.SeedWrite{{At: "0xB0000", U32: []uint32{40}}}, // ball_x=40, backs NearX
			Expect: evolution.ScenarioExpect{
				Draw:  &evolution.DrawExpect{Op: "circle", MinRecords: 1, NearX: &nx},
				Reads: []evolution.SeedWrite{{At: "0xB0008", Cmp: "decreased", U32: []uint32{5}}},
			}},
		// A render scenario whose ONLY assertion is a state change → nothing left to
		// check after the drop → dropped entirely.
		{Name: "render-only-statechange", Entry: "render-frame",
			Expect: evolution.ScenarioExpect{
				Reads: []evolution.SeedWrite{{At: "0xB0008", Cmp: "decreased", U32: []uint32{5}}},
			}},
	}
	out := groundScenarios(scs, c, true)

	var kept *evolution.Scenario
	for i := range out {
		if out[i].Name == "render-after-bounce" {
			kept = &out[i]
		}
		if out[i].Name == "render-only-statechange" {
			t.Fatal("a render scenario with only a state-change assertion must be dropped")
		}
	}
	if kept == nil {
		t.Fatal("the render+draw scenario should be kept (graded on the draw)")
	}
	for _, r := range kept.Expect.Reads {
		if r.Cmp == "decreased" {
			t.Fatalf("state-change reads-postcondition not dropped from render cell: %+v", kept.Expect.Reads)
		}
	}
}

package appgen

import (
	"testing"

	"github.com/mdcfrancis/flow/evolution"
)

func ballContract() *evolution.AppContract {
	return &evolution.AppContract{Fields: []evolution.ContractField{
		{Name: "ball_x", Offset: 0xB0000, Type: "i32", Init: 160},
		{Name: "ball_vx", Offset: 0xB0004, Type: "i32", Init: 3},
		{Name: "screen_width", Offset: 0xB0008, Type: "i32", Init: 320},
		{Name: "grid", Offset: 0xB0100, Type: "i32[40]"}, // array — no scalar init
	}}
}

// InitSeeds is the mock world: one seed per scalar field at its Init, arrays skipped.
func TestInitSeeds(t *testing.T) {
	seeds := ballContract().InitSeeds()
	got := map[string]uint32{}
	for _, s := range seeds {
		if len(s.U32) == 1 {
			got[s.At] = s.U32[0]
		}
	}
	if got["0xB0008"] != 320 {
		t.Fatalf("screen_width init seed missing/wrong: %v", got)
	}
	if got["0xB0004"] != 3 {
		t.Fatalf("ball_vx init seed missing/wrong: %v", got)
	}
	if _, ok := got["0xB0100"]; ok {
		t.Fatal("array field must not get a scalar init seed")
	}
}

// fillInit backstops a broken authored world: zero bounds/velocity become
// functional so the sim is never frozen from boot.
func TestFillInitBackstop(t *testing.T) {
	c := &evolution.AppContract{Fields: []evolution.ContractField{
		{Name: "screen_width", Offset: 0xB0000, Type: "i32", Init: 0},
		{Name: "screen_height", Offset: 0xB0004, Type: "i32", Init: 0},
		{Name: "ball_vx", Offset: 0xB0008, Type: "i32", Init: 0},
		{Name: "ball_x", Offset: 0xB000C, Type: "i32", Init: 0},
	}}
	fillInit(c)
	byName := map[string]int{}
	for _, f := range c.Fields {
		byName[f.Name] = f.Init
	}
	if byName["screen_width"] != 320 || byName["screen_height"] != 240 {
		t.Fatalf("screen bounds not backstopped: %v", byName)
	}
	if byName["ball_vx"] == 0 {
		t.Fatal("velocity must be non-zero so motion animates")
	}
	if byName["ball_x"] != 160 {
		t.Fatalf("ball_x should center to screen_width/2, got %d", byName["ball_x"])
	}
}

// The fix: a test case that seeds only the ball position now runs against the full
// mock world — screen_width comes from the init baseline, not zero.
func TestGroundScenariosInjectsInitBaseline(t *testing.T) {
	c := ballContract()
	// A scenario that only cares about ball_x — it does NOT seed screen_width.
	scs := []evolution.Scenario{{
		Name:   "ball_moves",
		Entry:  "run-tick",
		Seed:   []evolution.SeedWrite{{At: "0xB0000", U32: []uint32{50}}},
		Expect: evolution.ScenarioExpect{Reads: []evolution.SeedWrite{{At: "0xB0000", Cmp: "increased", U32: []uint32{50}}}},
	}}
	grounded := groundScenarios(scs, c, false)
	if len(grounded) != 1 {
		t.Fatalf("scenario was dropped: %+v", grounded)
	}
	// screen_width (0xB0008) must now be seeded from the init baseline...
	var sawScreen, sawBall bool
	for _, s := range grounded[0].Seed {
		if s.At == "0xB0008" && len(s.U32) == 1 && s.U32[0] == 320 {
			sawScreen = true
		}
		if s.At == "0xB0000" && len(s.U32) == 1 && s.U32[0] == 50 {
			sawBall = true // the scenario's own seed still present (and overrides baseline)
		}
	}
	if !sawScreen {
		t.Fatalf("init baseline (screen_width=320) not injected into the scenario: %+v", grounded[0].Seed)
	}
	if !sawBall {
		t.Fatalf("scenario's own ball_x=50 seed lost: %+v", grounded[0].Seed)
	}
}

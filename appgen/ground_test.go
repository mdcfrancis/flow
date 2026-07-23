package appgen

import (
	"testing"

	"github.com/mdcfrancis/flow/evolution"
)

func TestGroundScenariosResolvesFieldsAndDropsInvented(t *testing.T) {
	c := &evolution.AppContract{Fields: []evolution.ContractField{
		{Name: "player_x", Offset: 0xB0000, Type: "i32"},
	}}
	scs := []evolution.Scenario{
		{Name: "byfield", Expect: evolution.ScenarioExpect{Reads: []evolution.SeedWrite{{Field: "player_x", Cmp: "increased"}}}},
		{Name: "byname_in_at", Expect: evolution.ScenarioExpect{Reads: []evolution.SeedWrite{{At: "player_x", Cmp: "decreased"}}}},
		{Name: "invented_offset", Expect: evolution.ScenarioExpect{Reads: []evolution.SeedWrite{{At: "0x10000", Cmp: "increased"}}}},
		{Name: "no_reads", Expect: evolution.ScenarioExpect{}},
	}
	out := groundScenarios(scs, c, false) // run-tick coordination cell

	kept := map[string]evolution.Scenario{}
	for _, s := range out {
		kept[s.Name] = s
	}
	if _, ok := kept["invented_offset"]; ok {
		t.Error("scenario reading an out-of-contract offset (0x10000) must be dropped")
	}
	if _, ok := kept["no_reads"]; ok {
		t.Error("scenario with no reads is not a coordination check; dropped")
	}
	if s, ok := kept["byfield"]; !ok || s.Expect.Reads[0].At != "0xB0000" {
		t.Fatalf("byfield: resolved At=%q kept=%v, want 0xB0000", s.Expect.Reads[0].At, ok)
	}
	if s, ok := kept["byname_in_at"]; !ok || s.Expect.Reads[0].At != "0xB0000" {
		t.Fatalf("byname_in_at: resolved At=%q kept=%v, want 0xB0000", s.Expect.Reads[0].At, ok)
	}
}

func TestGroundScenariosDropsUnresolvedSeed(t *testing.T) {
	c := &evolution.AppContract{Fields: []evolution.ContractField{{Name: "player_x", Offset: 0xB0000, Type: "i32"}}}
	nx := 80
	scs := []evolution.Scenario{
		// seed uses a field name NOT in the contract -> unresolved -> dropped
		{Name: "broken_seed", Entry: "render-frame", Seed: []evolution.SeedWrite{{Field: "player_horizontal", U32: []uint32{80}}},
			Expect: evolution.ScenarioExpect{Draw: &evolution.DrawExpect{NearX: &nx}}},
		// seed uses a raw valid offset -> kept
		{Name: "good_seed", Entry: "render-frame", Seed: []evolution.SeedWrite{{At: "0xB0000", U32: []uint32{80}}},
			Expect: evolution.ScenarioExpect{Draw: &evolution.DrawExpect{NearX: &nx}}},
	}
	out := groundScenarios(scs, c, true) // renderer
	names := map[string]bool{}
	for _, s := range out {
		names[s.Name] = true
	}
	if names["broken_seed"] {
		t.Error("scenario with an unresolved field-name seed must be dropped")
	}
	if !names["good_seed"] {
		t.Error("scenario with a valid seed offset must be kept")
	}
}

// Rule 4: a draw / render-frame scenario on a non-rendering (run-tick) cell can
// never pass, so it must be dropped (the physics-cell-gets-a-render-check bug).
func TestGroundScenariosDropsDrawChecksOnNonRenderer(t *testing.T) {
	c := &evolution.AppContract{Fields: []evolution.ContractField{{Name: "dot_x", Offset: 0xB0000, Type: "i32"}}}
	nx := 50
	scs := []evolution.Scenario{
		{Name: "render_on_physics", Entry: "render-frame",
			Seed:   []evolution.SeedWrite{{At: "0xB0000", U32: []uint32{50}}},
			Expect: evolution.ScenarioExpect{Draw: &evolution.DrawExpect{NearX: &nx}}},
		{Name: "runtick_reads", Expect: evolution.ScenarioExpect{Reads: []evolution.SeedWrite{{At: "0xB0000", Cmp: "increased"}}}},
	}
	out := groundScenarios(scs, c, false) // NOT a renderer
	for _, s := range out {
		if s.Name == "render_on_physics" {
			t.Error("a render-frame/draw scenario on a non-renderer must be dropped")
		}
	}
	if len(out) != 1 || out[0].Name != "runtick_reads" {
		t.Fatalf("run-tick coordination check should survive; got %+v", out)
	}
}

// Rule 2: a draw-position assertion with no seed backing it is unverifiable
// (you never put the sprite where you assert it's drawn) — drop it.
func TestGroundScenariosDropsUnseededPositionCheck(t *testing.T) {
	c := &evolution.AppContract{Fields: []evolution.ContractField{
		{Name: "dot_x", Offset: 0xB0000, Type: "i32"},
		{Name: "dot_y", Offset: 0xB0004, Type: "i32"},
	}}
	nx, ny := 50, 60
	scs := []evolution.Scenario{
		{Name: "no_seed_pos", Entry: "render-frame",
			Expect: evolution.ScenarioExpect{Draw: &evolution.DrawExpect{NearX: &nx, NearY: &ny}}},
		{Name: "seeded_pos", Entry: "render-frame",
			Seed:   []evolution.SeedWrite{{At: "0xB0000", U32: []uint32{50}}, {At: "0xB0004", U32: []uint32{60}}},
			Expect: evolution.ScenarioExpect{Draw: &evolution.DrawExpect{NearX: &nx, NearY: &ny}}},
	}
	out := groundScenarios(scs, c, true)
	names := map[string]bool{}
	for _, s := range out {
		names[s.Name] = true
	}
	if names["no_seed_pos"] {
		t.Error("unseeded draw-position check must be dropped")
	}
	if !names["seeded_pos"] {
		t.Error("seed-backed draw-position check must be kept")
	}
}

// Rule 1: for a renderer, an exact reads-postcondition is promoted into a seed —
// the renderer doesn't produce the value, so the check must control it. This turns
// the "assert dot at 50 but never put it there" bug into a passing check.
func TestGroundScenariosPromotesRenderEqReadsToSeed(t *testing.T) {
	c := &evolution.AppContract{Fields: []evolution.ContractField{{Name: "dot_x", Offset: 0xB0000, Type: "i32"}}}
	nx := 50
	sc := evolution.Scenario{
		Name: "pos_a", Entry: "render-frame",
		Expect: evolution.ScenarioExpect{
			Draw:  &evolution.DrawExpect{NearX: &nx},
			Reads: []evolution.SeedWrite{{Field: "dot_x", U32: []uint32{50}, Cmp: "eq"}},
		},
	}
	out := groundScenarios([]evolution.Scenario{sc}, c, true)
	if len(out) != 1 {
		t.Fatalf("scenario should be kept after promotion, got %d", len(out))
	}
	seeded := false
	for _, s := range out[0].Seed {
		if s.At == "0xB0000" && len(s.U32) == 1 && s.U32[0] == 50 {
			seeded = true
		}
	}
	if !seeded {
		t.Errorf("eq reads-postcondition should be promoted to a seed; seeds=%+v", out[0].Seed)
	}
}

// Rule 2 (strong form): a draw NearX/NearY that matches no seeded value is a
// magic number a correct renderer can't be expected to hit — e.g. "window
// boundary rect at x=500" — even when there IS a (differently-valued) seed.
func TestGroundScenariosDropsMagicNumberPosition(t *testing.T) {
	c := &evolution.AppContract{Fields: []evolution.ContractField{
		{Name: "dot_x", Offset: 0xB0000, Type: "i32"},
	}}
	magic, matched := 500, 40
	scs := []evolution.Scenario{
		// seeds dot_x=40 but asserts a rect near x=500 -> unmatched -> dropped
		{Name: "boundary_rect", Entry: "render-frame",
			Seed:   []evolution.SeedWrite{{At: "0xB0000", U32: []uint32{40}}},
			Expect: evolution.ScenarioExpect{Draw: &evolution.DrawExpect{Op: "rect", NearX: &magic}}},
		// seeds dot_x=40 and asserts near x=40 -> matches -> kept
		{Name: "dot_rect", Entry: "render-frame",
			Seed:   []evolution.SeedWrite{{At: "0xB0000", U32: []uint32{40}}},
			Expect: evolution.ScenarioExpect{Draw: &evolution.DrawExpect{Op: "rect", NearX: &matched}}},
	}
	out := groundScenarios(scs, c, true)
	names := map[string]bool{}
	for _, s := range out {
		names[s.Name] = true
	}
	if names["boundary_rect"] {
		t.Error("a NearX matching no seeded value (magic number) must be dropped")
	}
	if !names["dot_rect"] {
		t.Error("a NearX equal to a seeded field value must be kept")
	}
}

// Rule 3: a draw target outside the window bounds is unreachable — drop it.
func TestGroundScenariosDropsOutOfBoundsDraw(t *testing.T) {
	c := &evolution.AppContract{Fields: []evolution.ContractField{
		{Name: "dot_x", Offset: 0xB0000, Type: "i32", Desc: "dot x"},
		{Name: "win_width", Offset: 0xB0010, Type: "i32", Desc: "window width, 0..300"},
		{Name: "win_height", Offset: 0xB0014, Type: "i32", Desc: "window height, 0..220"},
	}}
	out, in := 350, 120
	scs := []evolution.Scenario{
		{Name: "oob", Entry: "render-frame",
			Seed:   []evolution.SeedWrite{{At: "0xB0000", U32: []uint32{350}}},
			Expect: evolution.ScenarioExpect{Draw: &evolution.DrawExpect{NearX: &out}}},
		{Name: "inbounds", Entry: "render-frame",
			Seed:   []evolution.SeedWrite{{At: "0xB0000", U32: []uint32{120}}},
			Expect: evolution.ScenarioExpect{Draw: &evolution.DrawExpect{NearX: &in}}},
	}
	got := groundScenarios(scs, c, true)
	names := map[string]bool{}
	for _, s := range got {
		names[s.Name] = true
	}
	if names["oob"] {
		t.Error("draw target outside the window (350 > win_width 300) must be dropped")
	}
	if !names["inbounds"] {
		t.Error("in-bounds draw target must be kept")
	}
}

// The sustained-motion floor turns an accepted single-tick movement case into a
// multi-step check, but only for autonomous (non-input) movers.
func TestSustainedMotionFloor(t *testing.T) {
	// autonomous mover: seeds contract state, asserts a field increased, no input
	auto := &evolution.AcceptanceSuite{Scenarios: []evolution.Scenario{{
		Name: "normal_move", Entry: "run-tick",
		Seed:   []evolution.SeedWrite{{At: "0xB0000", U32: []uint32{100}}, {At: "0xB0008", U32: []uint32{5}}},
		Expect: evolution.ScenarioExpect{Reads: []evolution.SeedWrite{{At: "0xB0000", Cmp: "increased"}}},
	}}}
	f := sustainedMotionFloor(auto)
	if f == nil {
		t.Fatal("expected a sustained-motion floor for an autonomous mover")
	}
	if f.Steps < 2 {
		t.Errorf("floor must run multiple steps, got %d", f.Steps)
	}
	if len(f.Expect.Trajectory) != 1 || f.Expect.Trajectory[0].At != "0xB0000" || f.Expect.Trajectory[0].MinDistinct < 2 {
		t.Errorf("floor should assert a multi-value trajectory on the moved field, got %+v", f.Expect.Trajectory)
	}
	if len(f.Seed) != 2 {
		t.Errorf("floor should reuse the accepted seed, got %+v", f.Seed)
	}

	// input-driven cell: a seed in the HMI register -> NOT autonomous -> no floor
	inp := &evolution.AcceptanceSuite{Scenarios: []evolution.Scenario{{
		Name: "on_key", Entry: "run-tick",
		Seed:   []evolution.SeedWrite{{At: "0x50020", U32: []uint32{68}}, {At: "0xB0000", U32: []uint32{100}}},
		Expect: evolution.ScenarioExpect{Reads: []evolution.SeedWrite{{At: "0xB0000", Cmp: "increased"}}},
	}}}
	if sustainedMotionFloor(inp) != nil {
		t.Error("input-driven cell must NOT get a sustained-motion floor (moves only on input)")
	}

	// no movement assertion -> no floor
	still := &evolution.AcceptanceSuite{Scenarios: []evolution.Scenario{{
		Name: "reads_flag", Entry: "run-tick",
		Seed:   []evolution.SeedWrite{{At: "0xB0000", U32: []uint32{1}}},
		Expect: evolution.ScenarioExpect{Reads: []evolution.SeedWrite{{At: "0xB0000", Cmp: "unchanged"}}},
	}}}
	if sustainedMotionFloor(still) != nil {
		t.Error("a non-moving cell must not get a motion floor")
	}
}

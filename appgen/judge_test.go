package appgen

import (
	"testing"

	"github.com/mdcfrancis/flow/evolution"
)

func TestAutonomousMovementDetection(t *testing.T) {
	// autonomous mover: no-input seed, asserts a field moved
	auto := &evolution.AcceptanceSuite{Scenarios: []evolution.Scenario{{
		Entry: "run-tick",
		Seed:  []evolution.SeedWrite{{At: "0xB0000", U32: []uint32{100}}, {At: "0xB0008", U32: []uint32{5}}},
		Expect: evolution.ScenarioExpect{Reads: []evolution.SeedWrite{
			{At: "0xB0000", Cmp: "increased"}, {At: "0xB0004", Cmp: "increased"},
		}},
	}}}
	seed, offs := autonomousMovement(auto)
	if len(seed) != 2 || len(offs) != 2 {
		t.Fatalf("expected seed(2) + 2 moved offsets, got seed=%d offs=%v", len(seed), offs)
	}

	// input-driven: seed in HMI register -> not autonomous
	inp := &evolution.AcceptanceSuite{Scenarios: []evolution.Scenario{{
		Entry:  "run-tick",
		Seed:   []evolution.SeedWrite{{At: "0x50020", U32: []uint32{68}}},
		Expect: evolution.ScenarioExpect{Reads: []evolution.SeedWrite{{At: "0xB0000", Cmp: "increased"}}},
	}}}
	if _, offs := autonomousMovement(inp); offs != nil {
		t.Error("input-driven cell must not be treated as an autonomous mover")
	}
}

func TestDistinctU32(t *testing.T) {
	if distinctU32([]uint32{5, 5, 5}) != 1 {
		t.Error("frozen path should have 1 distinct")
	}
	if distinctU32([]uint32{0, 600, 0, 600}) != 2 {
		t.Error("two-value oscillation should have 2 distinct")
	}
	if distinctU32([]uint32{1, 2, 3, 4, 5}) != 5 {
		t.Error("traversal should count all distinct")
	}
}

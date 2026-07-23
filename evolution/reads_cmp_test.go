package evolution

import (
	"context"
	"testing"

	"github.com/mdcfrancis/flow/compiler"
)

// A run-tick cell that adds 10 to the i32 at 0xB0000 (player_x) each call.
const moveRightBy10 = `(module
  (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 100))
  (func (export "run-tick") (param i32 i32) (result i32)
    (i32.store (i32.const 720896)
      (i32.add (i32.load (i32.const 720896)) (i32.const 10)))
    (i32.const 0)))`

func scoreOne(t *testing.T, sc Scenario) int {
	t.Helper()
	art, err := compiler.NewCompilerService().CompileGenotype(moveRightBy10)
	if err != nil || !art.SyntaxPassed {
		t.Fatalf("compile: %v", err)
	}
	p, _ := ScenarioScore(context.Background(), art.Bytecode, []Scenario{sc}, DefaultPayloadOffset, DefaultStateWindow, nil)
	return p
}

// "increased" passes for a cell that moves right by ANY amount, even though the
// seed→exact scenario (==105) would have failed it (cell moves by 10, not 5).
func TestReadsIncreasedAcceptsAnyStep(t *testing.T) {
	seed := []SeedWrite{{At: "0xB0000", U32: []uint32{100}}}
	if scoreOne(t, Scenario{Name: "exact", Seed: seed,
		Expect: ScenarioExpect{Reads: []SeedWrite{{At: "0xB0000", U32: []uint32{105}}}}}) != 0 {
		t.Fatal("exact ==105 should FAIL a cell that moves by 10")
	}
	if scoreOne(t, Scenario{Name: "rel", Seed: seed,
		Expect: ScenarioExpect{Reads: []SeedWrite{{At: "0xB0000", Cmp: "increased"}}}}) != 1 {
		t.Fatal("cmp:increased should PASS a cell that moves right by any step")
	}
	// gt threshold also passes (110 > 100)
	if scoreOne(t, Scenario{Name: "gt", Seed: seed,
		Expect: ScenarioExpect{Reads: []SeedWrite{{At: "0xB0000", U32: []uint32{100}, Cmp: "gt"}}}}) != 1 {
		t.Fatal("cmp:gt 100 should PASS (final 110 > 100)")
	}
	// decreased should FAIL (it increased)
	if scoreOne(t, Scenario{Name: "dec", Seed: seed,
		Expect: ScenarioExpect{Reads: []SeedWrite{{At: "0xB0000", Cmp: "decreased"}}}}) != 0 {
		t.Fatal("cmp:decreased should FAIL an increasing cell")
	}
}

package evolution

import (
	"context"
	"testing"

	"github.com/mdcfrancis/flow/compiler"
)

// A cell that VERIFIABLY writes a shared field passes acceptance when graded
// unmasked, but if its DECLARED write ports omit that field the enforced mask
// reverts the write every step — so graded UNDER THE MASK the same cell FAILS.
// This is the "static ball" gap: acceptance must grade under the boundary the cell
// runs under, so a too-tight boundary surfaces as a failure (→ stall → boundary
// re-evolution) instead of a green commit that goes static live.
func TestAcceptanceGradesUnderBoundaryMask(t *testing.T) {
	// run-tick: unconditionally write player_x = 4 at 0xB0000.
	wat := `(module (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 100))
	  (func (export "run-tick") (param i32 i32) (result i32)
	    i32.const 720896 i32.const 4 i32.store
	    i32.const 0))`
	art, err := compiler.NewCompilerService().CompileGenotype(wat)
	if err != nil || !art.SyntaxPassed {
		t.Fatalf("compile: %v", err)
	}
	scen := []Scenario{{
		Name:   "writes player_x",
		Expect: ScenarioExpect{Reads: []SeedWrite{{At: "0xB0000", U32: []uint32{4}}}},
	}}
	ctx := context.Background()
	ct := &AppContract{Fields: []ContractField{{Name: "player_x", Offset: 0xB0000, Type: "i32"}}}

	// Unmasked: the write lands, the assertion holds.
	if p, tot := ScenarioScore(ctx, art.Bytecode, scen, DefaultPayloadOffset, DefaultStateWindow, nil); p != 1 || tot != 1 {
		t.Fatalf("unmasked: %d/%d, want 1/1", p, tot)
	}

	// Declared ports OMIT player_x → the mask reverts the write → the cell reads as
	// static → the assertion FAILS. This is the gap the fix closes.
	tootight := BuildFieldMask(&ComponentMap{DeclaredWrites: nil, DeclaredReads: nil}, ct)
	if tootight == nil {
		t.Fatal("a cell declaring no ports must be masked on player_x")
	}
	if p, _ := ScenarioScore(ctx, art.Bytecode, scen, DefaultPayloadOffset, DefaultStateWindow, nil, tootight); p != 0 {
		t.Fatalf("masked (too-tight ports): passed %d, want 0 (write must be reverted)", p)
	}

	// Declared ports INCLUDE player_x → nothing to enforce (BuildFieldMask nil) → the
	// correctly-ported cell is unaffected and still passes.
	ok := BuildFieldMask(&ComponentMap{DeclaredWrites: []string{"player_x"}}, ct)
	if ok != nil {
		t.Fatalf("a cell declaring player_x must not be masked on it: %+v", ok)
	}
	if p, _ := ScenarioScore(ctx, art.Bytecode, scen, DefaultPayloadOffset, DefaultStateWindow, nil, ok); p != 1 {
		t.Fatalf("masked (correct ports): passed %d, want 1", p)
	}
}

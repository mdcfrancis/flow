package evolution

import (
	"context"
	"testing"

	"github.com/mdcfrancis/flow/compiler"
)

// TestWastefulFuelProbe checks whether the fuel metric (function-entry count)
// actually distinguishes the seeded wasteful cell (three dead helper calls)
// from its optimized form (run-tick => i32.const 1). If it does not, the loop
// can never reward removing those calls and demo:wasteful is un-optimizable.
func TestWastefulFuelProbe(t *testing.T) {
	seeded := `(module
  (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 100))
  (func $spin1 (result i32) i32.const 7 i32.const 3 i32.mul drop i32.const 0)
  (func $spin2 (result i32) i32.const 11 i32.const 5 i32.add drop i32.const 0)
  (func $spin3 (result i32) i32.const 99 i32.popcnt drop i32.const 0)
  (func (export "run-tick") (param i32 i32) (result i32)
    call $spin1 drop call $spin2 drop call $spin3 drop i32.const 1))`
	lean := `(module
  (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 100))
  (func (export "run-tick") (param i32 i32) (result i32) i32.const 1))`

	cs := compiler.NewCompilerService()
	sa, err := cs.CompileGenotype(seeded)
	if err != nil || !sa.SyntaxPassed {
		t.Fatalf("seeded compile: %v", err)
	}
	la, err := cs.CompileGenotype(lean)
	if err != nil || !la.SyntaxPassed {
		t.Fatalf("lean compile: %v", err)
	}
	rs, err := execCell(context.Background(), sa.Bytecode, "run-tick", 0, 0)
	if err != nil {
		t.Fatalf("seeded exec: %v", err)
	}
	rl, err := execCell(context.Background(), la.Bytecode, "run-tick", 0, 0)
	if err != nil {
		t.Fatalf("lean exec: %v", err)
	}
	t.Logf("seeded fuel=%d  lean fuel=%d  (delta %d)", rs.Fuel, rl.Fuel, int(rs.Fuel)-int(rl.Fuel))
	// The whole demo rests on this: removing the three dead helper calls must
	// lower the fuel count. If wazero ever elides them (inlining) this metric
	// would go blind and demo:wasteful could never be optimized — ΔH always 0.
	if rs.Fuel <= rl.Fuel {
		t.Fatalf("fuel metric is blind to dead calls: seeded=%d lean=%d (want seeded > lean)", rs.Fuel, rl.Fuel)
	}
	// Concretely: one entry per helper + the entry itself. A live wasteful cell
	// reported at N*1 fuel across N corpus cases is ALREADY optimized (lean);
	// N*4 would still be wasteful.
	if rl.Fuel != 1 || rs.Fuel != 4 {
		t.Logf("note: fuel-per-tick lean=%d seeded=%d (metric = guest function entries)", rl.Fuel, rs.Fuel)
	}
}

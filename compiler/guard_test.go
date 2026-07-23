package compiler

import (
	"context"
	"testing"

	"github.com/tetratelabs/wazero"
)

// TestMemoryGuardClampsStores compiles a cell that stores to a caller-supplied
// address under a [0,256) memory guard, and verifies in-bounds stores succeed
// while out-of-bounds stores trap.
func TestMemoryGuardClampsStores(t *testing.T) {
	ctx := context.Background()
	// run-tick(addr, _) stores 42 at addr, then loads it back.
	wat := `(module
	  (memory (export "mem") 1)
	  (func (export "run-tick") (param $addr i32) (param $len i32) (result i32)
	    local.get $addr i32.const 42 i32.store
	    local.get $addr i32.load))`

	guarded := NewGuardedCompilerService(0, 256)
	art, err := guarded.CompileGenotype(wat)
	if err != nil || !art.SyntaxPassed {
		t.Fatalf("guarded compile: %v (%s)", err, art.ErrorContext)
	}

	r := wazero.NewRuntime(ctx)
	defer r.Close(ctx)
	mod, err := r.Instantiate(ctx, art.Bytecode)
	if err != nil {
		t.Fatalf("wazero rejected guarded module: %v", err)
	}
	fn := mod.ExportedFunction("run-tick")

	// In bounds: addr 100 < 256 -> stores and reads back 42.
	res, err := fn.Call(ctx, 100, 0)
	if err != nil {
		t.Fatalf("in-bounds store trapped: %v", err)
	}
	if res[0] != 42 {
		t.Fatalf("in-bounds result = %d, want 42", res[0])
	}

	// Out of bounds: addr 300 >= 256 -> guard traps.
	if _, err := fn.Call(ctx, 300, 0); err == nil {
		t.Fatal("out-of-bounds store should have trapped under the guard")
	}

	// Without the guard, the same out-of-bounds store succeeds (proves the
	// guard is what traps, not wasm's own bounds — 300 is within 1 page).
	unart, _ := NewCompilerService().CompileGenotype(wat)
	umod, _ := r.Instantiate(ctx, unart.Bytecode)
	if _, err := umod.ExportedFunction("run-tick").Call(ctx, 300, 0); err != nil {
		t.Fatalf("unguarded store at 300 should succeed: %v", err)
	}
}

package execution

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mdcfrancis/flow/compiler"
	"github.com/mdcfrancis/flow/manifest"
	"github.com/mdcfrancis/flow/storage"
)

// TestInterCellDispatch proves one cell can invoke another by URN across the
// cell-dispatch host bridge: the caller dispatches to a callee that returns 7,
// and the caller returns that value.
func TestInterCellDispatch(t *testing.T) {
	ctx := context.Background()
	le, err := storage.NewLedgerEngine(filepath.Join(t.TempDir(), "hdm.db"))
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	defer le.Close()

	rm, err := NewRuntimeManager(ctx, le, nil)
	if err != nil {
		t.Fatalf("runtime: %v", err)
	}
	defer rm.Close(ctx)

	cs := compiler.NewCompilerService()
	repo := manifest.NewRepository(le)

	// Callee returns 7.
	calleeWAT := `(module
	  (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 100))
	  (func (export "run-tick") (param i32 i32) (result i32) i32.const 7))`
	calleeArt, _ := cs.CompileGenotype(calleeWAT)
	cd, _, _ := repo.PutCell("urn:hdm:cell:callee", calleeWAT, calleeArt.Bytecode, manifest.SemanticManifest{}, 0)
	repo.SeedRef("urn:hdm:cell:callee", cd)

	// Caller writes the callee URN into memory and dispatches to it. The URN
	// "urn:hdm:cell:callee" is 19 bytes, placed at offset 256.
	callerWAT := `(module
	  (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 100))
	  (import "hdm:kernel/cell-dispatch" "invoke-cell"
	    (func $dispatch (param i32 i32 i32 i32) (result i32)))
	  (data (i32.const 256) "urn:hdm:cell:callee")
	  (func (export "run-tick") (param i32 i32) (result i32)
	    i32.const 256 i32.const 19
	    i32.const 0 i32.const 0
	    call $dispatch))`
	callerArt, err := cs.CompileGenotype(callerWAT)
	if err != nil || !callerArt.SyntaxPassed {
		t.Fatalf("compile caller: %v (%s)", err, callerArt.ErrorContext)
	}
	if err := rm.LoadCell("urn:hdm:cell:caller", callerArt.Bytecode); err != nil {
		t.Fatalf("load caller: %v", err)
	}

	res, fuel, _, err := rm.ExecuteTrampoline("host", "urn:hdm:cell:caller", "run-tick", 0, 0)
	if err != nil {
		t.Fatalf("trampoline: %v", err)
	}
	if res != 7 {
		t.Fatalf("dispatch result = %d, want 7 (callee value)", res)
	}
	// Fuel should reflect both the caller and the dispatched callee crossing.
	if fuel < 2 {
		t.Fatalf("fuel = %d, expected >=2 (caller + dispatched callee)", fuel)
	}
}

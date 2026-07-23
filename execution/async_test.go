package execution

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mdcfrancis/flow/compiler"
	"github.com/mdcfrancis/flow/inference"
	"github.com/mdcfrancis/flow/storage"
)

// syncCell is a plain run-tick cell — no cognitive-engine import.
const syncCell = `(module
  (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 100))
  (func (export "run-tick") (param i32 i32) (result i32) i32.const 1))`

// TestIsAsyncCellClassifiesByReasoningImport verifies a cell importing the
// cognitive engine is classified ASYNC (runs off the frame thread) and a plain
// compute cell is SYNC.
func TestIsAsyncCellClassifiesByReasoningImport(t *testing.T) {
	ctx := context.Background()
	le, err := storage.NewLedgerEngine(filepath.Join(t.TempDir(), "hdm.db"))
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	defer le.Close()
	rm, err := NewRuntimeManager(ctx, le, inference.NewLocalModelClient("http://127.0.0.1:0", "test"))
	if err != nil {
		t.Fatalf("runtime: %v", err)
	}
	defer rm.Close(ctx)

	cs := compiler.NewCompilerService()
	async, err := cs.CompileGenotype(reasonerCell)
	if err != nil || !async.SyntaxPassed {
		t.Fatalf("compile async: %v", err)
	}
	sync, err := cs.CompileGenotype(syncCell)
	if err != nil || !sync.SyntaxPassed {
		t.Fatalf("compile sync: %v", err)
	}

	if !rm.IsAsyncCell("hash-async", async.Bytecode) {
		t.Error("a cognitive-engine-importing cell must be classified ASYNC")
	}
	if rm.IsAsyncCell("hash-sync", sync.Bytecode) {
		t.Error("a plain run-tick cell must be classified SYNC")
	}
	// memoized: second call returns the cached verdict (no recompile needed)
	if !rm.IsAsyncCell("hash-async", nil) {
		t.Error("classification must be memoized by hash")
	}
}

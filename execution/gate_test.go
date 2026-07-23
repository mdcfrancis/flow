package execution

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/mdcfrancis/flow/compiler"
	"github.com/mdcfrancis/flow/inference"
	"github.com/mdcfrancis/flow/storage"
)

// reasonerCell calls the cognitive engine once per run-tick, like the bootstrap
// optimizer does.
const reasonerCell = `(module
  (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 100))
  (import "hdm:kernel/cognitive-engine" "invoke-reasoning"
    (func $reason (param i32 i32 i32 i32) (result i32 i32)))
  (func (export "run-tick") (param i32 i32) (result i32)
    (call $reason (i32.const 0) (i32.const 0) (i32.const 0) (i32.const 0))
    drop drop
    i32.const 1))`

func TestReasoningGateStopsModelCalls(t *testing.T) {
	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"total_tokens":7}}`))
	}))
	defer srv.Close()

	ctx := context.Background()
	le, err := storage.NewLedgerEngine(filepath.Join(t.TempDir(), "hdm.db"))
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	defer le.Close()
	rm, err := NewRuntimeManager(ctx, le, inference.NewLocalModelClient(srv.URL, "test-model"))
	if err != nil {
		t.Fatalf("runtime: %v", err)
	}
	defer rm.Close(ctx)

	art, err := compiler.NewCompilerService().CompileGenotype(reasonerCell)
	if err != nil || !art.SyntaxPassed {
		t.Fatalf("compile: %v (%s)", err, art.ErrorContext)
	}
	if err := rm.LoadCell("urn:hdm:test:reasoner", art.Bytecode); err != nil {
		t.Fatalf("load: %v", err)
	}

	// Ungated: the tick reaches the model.
	if _, _, _, err := rm.ExecuteTrampoline("urn:hdm:sys:host", "urn:hdm:test:reasoner", "run-tick", 0, 0); err != nil {
		t.Fatalf("ungated tick: %v", err)
	}
	if got := atomic.LoadInt64(&hits); got != 1 {
		t.Fatalf("ungated model hits = %d, want 1", got)
	}

	// Gated: the tick still runs, but the model is never contacted.
	rm.SetReasoningGate(true)
	for i := 0; i < 3; i++ {
		if _, _, _, err := rm.ExecuteTrampoline("urn:hdm:sys:host", "urn:hdm:test:reasoner", "run-tick", 0, 0); err != nil {
			t.Fatalf("gated tick %d: %v", i, err)
		}
	}
	if got := atomic.LoadInt64(&hits); got != 1 {
		t.Fatalf("model hits after gating = %d, want 1 (gate should suppress all further calls)", got)
	}

	// Ungating resumes model contact.
	rm.SetReasoningGate(false)
	if _, _, _, err := rm.ExecuteTrampoline("urn:hdm:sys:host", "urn:hdm:test:reasoner", "run-tick", 0, 0); err != nil {
		t.Fatalf("re-ungated tick: %v", err)
	}
	if got := atomic.LoadInt64(&hits); got != 2 {
		t.Fatalf("model hits after ungating = %d, want 2", got)
	}
}

package execution

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/mdcfrancis/flow/compiler"
	"github.com/mdcfrancis/flow/inference"
	"github.com/mdcfrancis/flow/storage"
)

// tickCell returns the current chronos.tick() truncated to i32.
const tickCell = `(module
  (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 100))
  (import "hdm:kernel/chronos" "tick" (func $tick (result i64)))
  (func (export "run-tick") (param i32 i32) (result i32)
    call $tick
    i32.wrap_i64))`

// randCell returns the next chronos.random() truncated to i32.
const randCell = `(module
  (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 100))
  (import "hdm:kernel/chronos" "random" (func $rand (result i64)))
  (func (export "run-tick") (param i32 i32) (result i32)
    call $rand
    i32.wrap_i64))`

func newRMModel(t *testing.T, modelURL string) *RuntimeManager {
	t.Helper()
	le, err := storage.NewLedgerEngine(filepath.Join(t.TempDir(), "hdm.db"))
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	t.Cleanup(func() { le.Close() })
	rm, err := NewRuntimeManager(context.Background(), le, inference.NewLocalModelClient(modelURL, "test"))
	if err != nil {
		t.Fatalf("runtime: %v", err)
	}
	t.Cleanup(func() { rm.Close(context.Background()) })
	return rm
}

func loadCell(t *testing.T, rm *RuntimeManager, urn, wat string) {
	t.Helper()
	art, err := compiler.NewCompilerService().CompileGenotype(wat)
	if err != nil || !art.SyntaxPassed {
		t.Fatalf("compile %s: %v (%s)", urn, err, art.ErrorContext)
	}
	if err := rm.LoadCell(urn, art.Bytecode); err != nil {
		t.Fatalf("load %s: %v", urn, err)
	}
}

func TestChronosTickCounter(t *testing.T) {
	rm, _ := newRM(t)
	loadCell(t, rm, "urn:hdm:test:tick", tickCell)

	rm.AdvanceTick()
	rm.AdvanceTick()
	rm.AdvanceTick()
	if rm.Tick() != 3 {
		t.Fatalf("Tick() = %d, want 3", rm.Tick())
	}
	res, _, _, err := rm.ExecuteTrampoline("urn:hdm:sys:host", "urn:hdm:test:tick", "run-tick", 0, 0)
	if err != nil {
		t.Fatalf("tick cell: %v", err)
	}
	if res != 3 {
		t.Fatalf("guest chronos.tick() = %d, want 3 (the heartbeat beat)", res)
	}
	// The beat advances.
	rm.AdvanceTick()
	res, _, _, _ = rm.ExecuteTrampoline("urn:hdm:sys:host", "urn:hdm:test:tick", "run-tick", 0, 0)
	if res != 4 {
		t.Fatalf("after AdvanceTick, guest tick = %d, want 4", res)
	}
}

func TestChronosRandomStreamVaries(t *testing.T) {
	rm, _ := newRM(t)
	loadCell(t, rm, "urn:hdm:test:rand", randCell)
	seen := map[uint32]bool{}
	for i := 0; i < 8; i++ {
		res, _, _, err := rm.ExecuteTrampoline("urn:hdm:sys:host", "urn:hdm:test:rand", "run-tick", 0, 0)
		if err != nil {
			t.Fatalf("rand cell: %v", err)
		}
		seen[res] = true
	}
	// A real stream: 8 successive draws should not collapse to one value (as the
	// static entropy() byte would). Allow a rare collision but not all-identical.
	if len(seen) < 4 {
		t.Fatalf("chronos.random() produced only %d distinct values across 8 draws — not a stream", len(seen))
	}
}

// TestReasoningDoesNotBlockTicks verifies the non-blocking model: while a cell is
// parked in a slow invoke-reasoning call, a concurrent tick on another cell still
// runs (rm.mu is released around the model round-trip) instead of freezing.
func TestReasoningDoesNotBlockTicks(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release // hold the reasoning call open
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"total_tokens":1}}`))
	}))
	defer srv.Close()

	rm := newRMModel(t, srv.URL)
	loadCell(t, rm, "urn:hdm:test:reasoner", reasonerCell)
	loadCell(t, rm, "urn:hdm:test:fast", tickCell) // any non-reasoning cell

	// Fire the slow reasoning tick; it will block inside the model call.
	go func() {
		_, _, _, _ = rm.ExecuteTrampoline("urn:hdm:sys:host", "urn:hdm:test:reasoner", "run-tick", 0, 0)
	}()
	<-entered // reasoning is now inside the model round-trip (lock released)

	done := make(chan error, 1)
	go func() {
		_, _, _, e := rm.ExecuteTrampoline("urn:hdm:sys:host", "urn:hdm:test:fast", "run-tick", 0, 0)
		done <- e
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("concurrent tick errored: %v", err)
		}
	case <-time.After(3 * time.Second):
		close(release)
		t.Fatal("a regular tick blocked behind an in-flight reasoning call — inference is not non-blocking")
	}
	close(release) // let the reasoning call finish
}

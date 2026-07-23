package execution

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mdcfrancis/flow/compiler"
	"github.com/mdcfrancis/flow/storage"
)

// TestBlockStorageHostRoundTrip drives a guest cell — assembled by the native
// WAT compiler — that writes a data segment into the shared cluster memory,
// calls block-storage.write-block to persist it (receiving a content hash back
// in scratch), then block-storage.read-block to retrieve it by that hash, and
// returns the length of the retrieved payload. A result equal to the original
// length proves the full host ABI (memory read, CAS write, scratch return,
// memory read of the hash, CAS read, scratch return) works end to end.
func TestBlockStorageHostRoundTrip(t *testing.T) {
	ctx := context.Background()
	le, err := storage.NewLedgerEngine(filepath.Join(t.TempDir(), "hdm.db"))
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	defer le.Close()

	rm, err := NewRuntimeManager(ctx, le, nil)
	if err != nil {
		t.Fatalf("runtime manager: %v", err)
	}
	defer rm.Close(ctx)

	// "HELLO-HDM" is 9 bytes, written at the inbound packet-frame offset.
	wat := `(module
	  (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 100))
	  (import "hdm:kernel/block-storage" "write-block" (func $wb (param i32 i32) (result i32 i32)))
	  (import "hdm:kernel/block-storage" "read-block" (func $rb (param i32 i32) (result i32 i32)))
	  (data (i32.const 65536) "HELLO-HDM")
	  (func (export "run-tick") (param i32 i32) (result i32)
	    (local $hp i32) (local $hl i32) (local $dp i32) (local $dl i32)
	    i32.const 65536 i32.const 9
	    call $wb
	    local.set $hl local.set $hp
	    local.get $hp local.get $hl
	    call $rb
	    local.set $dl local.set $dp
	    local.get $dl))`

	art, err := compiler.NewCompilerService().CompileGenotype(wat)
	if err != nil || !art.SyntaxPassed {
		t.Fatalf("compile guest: %v (%s)", err, art.ErrorContext)
	}
	const urn = "urn:hdm:cell:storage-probe"
	if err := rm.LoadCell(urn, art.Bytecode); err != nil {
		t.Fatalf("load cell: %v", err)
	}

	res, fuel, _, err := rm.ExecuteTrampoline("urn:hdm:sys:host", urn, "run-tick", 0, 0)
	if err != nil {
		t.Fatalf("trampoline: %v", err)
	}
	if res != 9 {
		t.Fatalf("round-trip length = %d, want 9", res)
	}
	if fuel == 0 {
		t.Fatal("expected non-zero fuel from instrumented execution")
	}
}

package execution

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mdcfrancis/flow/compiler"
	"github.com/mdcfrancis/flow/inference"
	"github.com/mdcfrancis/flow/storage"
)

// A micro-tick burst advances a cell N internal steps in ONE scheduling slot: a counter
// cell (each tick: mem[0xB8000] += 1) reaches N after a single TickAppCellN(N) call —
// proving iterative work isn't capped at one step per frame. Enrollment default is 1.
func TestMicroTickBurst(t *testing.T) {
	ctx := context.Background()
	le, _ := storage.NewLedgerEngine(filepath.Join(t.TempDir(), "hdm.db"))
	defer le.Close()
	rm, err := NewRuntimeManager(ctx, le, inference.NewLocalModelClient("http://127.0.0.1:0", "test"))
	if err != nil {
		t.Fatalf("runtime: %v", err)
	}
	defer rm.Close(ctx)
	cs := compiler.NewCompilerService()
	// run-tick: mem[0xB8000] = mem[0xB8000] + 1
	wat := `(module
  (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 16))
  (func (export "run-tick") (param i32 i32) (result i32)
    (i32.store (i32.const 0xB8000) (i32.add (i32.load (i32.const 0xB8000)) (i32.const 1)))
    (i32.load (i32.const 0xB8000))))`
	art, cerr := cs.CompileGenotype(wat)
	if cerr != nil || !art.SyntaxPassed {
		t.Fatalf("compile: %v", cerr)
	}
	const urn = "urn:hdm:test:counter"

	// One TickAppCellN(10) call → counter advances 10 in a single burst.
	res, _, _, err := rm.TickAppCellN("h", urn, "hash-counter", art.Bytecode, 10)
	if err != nil {
		t.Fatalf("burst: %v", err)
	}
	if res != 10 {
		t.Errorf("after a burst of 10, counter = %d, want 10", res)
	}
	if v, _ := rm.PeekU32(0xB8000); v != 10 {
		t.Errorf("shared counter = %d, want 10", v)
	}
	// A single TickAppCell continues from persisted state (11).
	if res, _, _, _ := rm.TickAppCell("h", urn, "hash-counter", art.Bytecode); res != 11 {
		t.Errorf("single tick after burst = %d, want 11", res)
	}

	// Enrollment: default is 1; enrolling sets it; n<=1 clears it.
	if n := rm.MicroTicksFor(urn); n != 1 {
		t.Errorf("default micro-ticks = %d, want 1", n)
	}
	rm.EnrollMicroTick(urn, 8)
	if n := rm.MicroTicksFor(urn); n != 8 {
		t.Errorf("enrolled micro-ticks = %d, want 8", n)
	}
	rm.EnrollMicroTick(urn, 1)
	if n := rm.MicroTicksFor(urn); n != 1 {
		t.Errorf("cleared micro-ticks = %d, want 1", n)
	}
}

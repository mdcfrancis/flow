package execution

import (
	"context"
	"encoding/binary"
	"math/rand"
	"path/filepath"
	"testing"

	"github.com/mdcfrancis/flow/compiler"
	"github.com/mdcfrancis/flow/inference"
	"github.com/mdcfrancis/flow/storage"
)

// TestSysDictDifferentialVsOracle drives sys:dict through a randomized op sequence and checks
// every get against a trusted Go map — the reference oracle. The dict's state lives entirely
// in its private page, round-tripped through page-in/page-out each op, so this also proves the
// page persists a real data structure across many ticks.
func TestSysDictDifferentialVsOracle(t *testing.T) {
	ctx := context.Background()
	le, _ := storage.NewLedgerEngine(filepath.Join(t.TempDir(), "hdm.db"))
	defer le.Close()
	rm, err := NewRuntimeManager(ctx, le, inference.NewLocalModelClient("http://127.0.0.1:0", "test"))
	if err != nil {
		t.Fatalf("runtime: %v", err)
	}
	defer rm.Close(ctx)
	cs := compiler.NewCompilerService()
	loadWAT(t, rm, cs, SysDictURN, SysDictWAT)

	h, err := rm.AllocPage(pageSize)
	if err != nil {
		t.Fatalf("dict page: %v", err)
	}
	rm.MapPage(SysDictURN, h)

	const argPtr = 0x000B0000 // a public scratch offset for the op buffer
	op := func(op, key, val uint32) uint32 {
		t.Helper()
		var b [12]byte
		binary.LittleEndian.PutUint32(b[0:], op)
		binary.LittleEndian.PutUint32(b[4:], key)
		binary.LittleEndian.PutUint32(b[8:], val)
		rm.sharedMem.Write(argPtr, b[:])
		res, _, _, err := rm.ExecuteTrampoline("h", SysDictURN, "run-tick", argPtr, 12)
		if err != nil {
			t.Fatalf("dict op: %v", err)
		}
		return res
	}

	oracle := map[uint32]uint32{}
	rng := rand.New(rand.NewSource(1)) // deterministic
	for step := 0; step < 4000; step++ {
		key := uint32(1 + rng.Intn(300)) // keys 1..300, dense enough to force collisions
		if rng.Intn(3) == 0 {            // ~1/3 gets, ~2/3 inserts
			got := op(0, key, 0)
			if got != oracle[key] {
				t.Fatalf("step %d get(%d)=%d, oracle=%d", step, key, got, oracle[key])
			}
		} else {
			val := uint32(rng.Uint32() | 1) // nonzero values
			if r := op(1, key, val); r != 1 {
				t.Fatalf("insert(%d,%d) returned %d, want 1", key, val, r)
			}
			oracle[key] = val
		}
	}
	// Final sweep: every key the oracle knows must read back exactly.
	for key, want := range oracle {
		if got := op(0, key, 0); got != want {
			t.Fatalf("final get(%d)=%d, want %d", key, got, want)
		}
	}
}

// TestSysDictThroughDispatchPreservesCallerWindow proves dispatch-path paging: a caller cell
// with its OWN private page invokes the dict via invoke-cell; the dict operates on its own
// page, and the caller's window is intact afterward (the runtime context-switched the window).
func TestSysDictThroughDispatchPreservesCallerWindow(t *testing.T) {
	ctx := context.Background()
	le, _ := storage.NewLedgerEngine(filepath.Join(t.TempDir(), "hdm.db"))
	defer le.Close()
	rm, err := NewRuntimeManager(ctx, le, inference.NewLocalModelClient("http://127.0.0.1:0", "test"))
	if err != nil {
		t.Fatalf("runtime: %v", err)
	}
	defer rm.Close(ctx)
	cs := compiler.NewCompilerService()
	loadWAT(t, rm, cs, SysDictURN, SysDictWAT)

	// The host pre-writes the dict URN + a get-arg buffer into public scratch; the caller
	// passes those pointers to invoke-cell. The caller stamps 0xCAFE into its OWN window,
	// dispatches the dict, then verifies its window survived and returns the dict's result.
	const urnPtr = 0x000B1000
	const argPtr = 0x000B2000
	rm.sharedMem.Write(urnPtr, []byte(SysDictURN))
	var arg [12]byte
	binary.LittleEndian.PutUint32(arg[0:], 0) // op = GET
	binary.LittleEndian.PutUint32(arg[4:], 7) // key = 7
	binary.LittleEndian.PutUint32(arg[8:], 0) // val (unused)
	rm.sharedMem.Write(argPtr, arg[:])

	caller := `(module
  (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 100))
  (import "hdm:kernel/cell-dispatch" "invoke-cell" (func $invoke (param i32 i32 i32 i32) (result i32)))
  (func (export "run-tick") (param i32 i32) (result i32)
    (local $r i32)
    (i32.store (i32.const 0x00400000) (i32.const 0xCAFE))
    (local.set $r (call $invoke (i32.const 0x000B1000) (i32.const ` + itoa(len(SysDictURN)) + `) (i32.const 0x000B2000) (i32.const 12)))
    (if (i32.ne (i32.load (i32.const 0x00400000)) (i32.const 0xCAFE))
      (then (return (i32.const 305419896)))) ;; 0x12345678 sentinel: window was clobbered
    (local.get $r)))`
	loadWAT(t, rm, cs, "urn:hdm:test:dict-caller", caller)

	// Two distinct pages: the caller's and the dict's.
	hCaller, _ := rm.AllocPage(pageSize)
	hDict, _ := rm.AllocPage(pageSize)
	rm.MapPage("urn:hdm:test:dict-caller", hCaller)
	rm.MapPage(SysDictURN, hDict)

	// Seed the dict (top-level op persists to the dict's page): insert 7 -> 99.
	var ins [12]byte
	binary.LittleEndian.PutUint32(ins[0:], 1)  // INSERT
	binary.LittleEndian.PutUint32(ins[4:], 7)  // key
	binary.LittleEndian.PutUint32(ins[8:], 99) // val
	rm.sharedMem.Write(argPtr, ins[:])
	if r, _, _, err := rm.ExecuteTrampoline("h", SysDictURN, "run-tick", argPtr, 12); err != nil || r != 1 {
		t.Fatalf("seed insert: r=%d err=%v", r, err)
	}
	// Restore the GET arg buffer the caller will pass.
	rm.sharedMem.Write(argPtr, arg[:])

	res, _, _, err := rm.ExecuteTrampoline("h", "urn:hdm:test:dict-caller", "run-tick", 0, 0)
	if err != nil {
		t.Fatalf("caller: %v", err)
	}
	if res == 305419896 {
		t.Fatalf("caller's window was clobbered by the dispatched dict — context switch failed")
	}
	if res != 99 {
		t.Fatalf("dict-get(7) through dispatch = %d, want 99", res)
	}
}

// itoa avoids a strconv import in the WAT string above.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

package execution

import (
	"context"
	"encoding/binary"
	"path/filepath"
	"testing"

	"github.com/mdcfrancis/flow/compiler"
	"github.com/mdcfrancis/flow/inference"
	"github.com/mdcfrancis/flow/storage"
)

// TestGuestDrivenPageSelection proves paging.select: a single caller cell drives TWO dict
// instances, choosing which page each dispatched op runs against. The same key holds different
// values in the two dicts, which is only possible if the guest's per-dispatch selection works.
func TestGuestDrivenPageSelection(t *testing.T) {
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

	// Host lays out, in public scratch: the dict URN, three op buffers, and the two handles.
	const urnPtr = 0x000B1000
	const argIns1 = 0x000B2000 // [INSERT, 5, 100]
	const argIns2 = 0x000B2010 // [INSERT, 5, 200]
	const argGet = 0x000B2020  // [GET, 5, 0]
	const handles = 0x000B3000 // [h1, h2]
	rm.sharedMem.Write(urnPtr, []byte(SysDictURN))
	put3 := func(off uint32, a, b, c uint32) {
		var buf [12]byte
		binary.LittleEndian.PutUint32(buf[0:], a)
		binary.LittleEndian.PutUint32(buf[4:], b)
		binary.LittleEndian.PutUint32(buf[8:], c)
		rm.sharedMem.Write(off, buf[:])
	}
	put3(argIns1, 1, 5, 100)
	put3(argIns2, 1, 5, 200)
	put3(argGet, 0, 5, 0)

	h1, _ := rm.AllocPage(pageSize)
	h2, _ := rm.AllocPage(pageSize)
	var hb [8]byte
	binary.LittleEndian.PutUint32(hb[0:], h1)
	binary.LittleEndian.PutUint32(hb[4:], h2)
	rm.sharedMem.Write(handles, hb[:])

	// The caller: select(h1)->insert(5,100); select(h2)->insert(5,200); then
	// select(h1)->get(5) and select(h2)->get(5); return 1 iff they read 100 and 200.
	ulen := itoa(len(SysDictURN))
	caller := `(module
  (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 100))
  (import "hdm:kernel/cell-dispatch" "invoke-cell" (func $invoke (param i32 i32 i32 i32) (result i32)))
  (import "hdm:kernel/paging" "select" (func $select (param i32) (result i32)))
  (func (export "run-tick") (param i32 i32) (result i32)
    (local $h1 i32) (local $h2 i32) (local $r1 i32) (local $r2 i32)
    (local.set $h1 (i32.load (i32.const 0x000B3000)))
    (local.set $h2 (i32.load (i32.const 0x000B3004)))
    (drop (call $select (local.get $h1)))
    (drop (call $invoke (i32.const 0x000B1000) (i32.const ` + ulen + `) (i32.const 0x000B2000) (i32.const 12)))
    (drop (call $select (local.get $h2)))
    (drop (call $invoke (i32.const 0x000B1000) (i32.const ` + ulen + `) (i32.const 0x000B2010) (i32.const 12)))
    (drop (call $select (local.get $h1)))
    (local.set $r1 (call $invoke (i32.const 0x000B1000) (i32.const ` + ulen + `) (i32.const 0x000B2020) (i32.const 12)))
    (drop (call $select (local.get $h2)))
    (local.set $r2 (call $invoke (i32.const 0x000B1000) (i32.const ` + ulen + `) (i32.const 0x000B2020) (i32.const 12)))
    (i32.and (i32.eq (local.get $r1) (i32.const 100)) (i32.eq (local.get $r2) (i32.const 200)))))`
	loadWAT(t, rm, cs, "urn:hdm:test:multi-dict", caller)

	res, _, _, err := rm.ExecuteTrampoline("h", "urn:hdm:test:multi-dict", "run-tick", 0, 0)
	if err != nil {
		t.Fatalf("caller: %v", err)
	}
	if res != 1 {
		t.Fatalf("guest-driven selection failed: key 5 did not hold 100 in dict1 AND 200 in dict2 (res=%d)", res)
	}
}

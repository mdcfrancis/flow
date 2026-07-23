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

// TestPageOwnershipBeatsHandlePossession proves that a page allocated by one cell (its owner)
// cannot be selected by another cell even if that cell learns the handle bits — ownership, not
// handle secrecy, is the boundary.
func TestPageOwnershipBeatsHandlePossession(t *testing.T) {
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

	const urnPtr = 0x000B1000
	const argIns = 0x000B2000 // [INSERT, 5, 111]
	const argGet = 0x000B2010 // [GET, 5, 0]
	const handleSlot = 0x000B3000
	rm.sharedMem.Write(urnPtr, []byte(SysDictURN))
	put3 := func(off, a, b, c uint32) {
		var buf [12]byte
		binary.LittleEndian.PutUint32(buf[0:], a)
		binary.LittleEndian.PutUint32(buf[4:], b)
		binary.LittleEndian.PutUint32(buf[8:], c)
		rm.sharedMem.Write(off, buf[:])
	}
	put3(argIns, 1, 5, 111)
	put3(argGet, 0, 5, 0)
	ulen := itoa(len(SysDictURN))

	// Owner: allocate a private page (owner = self), publish the handle at handleSlot (a leak),
	// select it and insert 5 -> 111. Returns the handle.
	owner := `(module
  (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 100))
  (import "hdm:kernel/cell-dispatch" "invoke-cell" (func $invoke (param i32 i32 i32 i32) (result i32)))
  (import "hdm:kernel/paging" "alloc" (func $alloc (param i32) (result i32)))
  (import "hdm:kernel/paging" "select" (func $select (param i32) (result i32)))
  (func (export "run-tick") (param i32 i32) (result i32)
    (local $h i32)
    (local.set $h (call $alloc (i32.const 4096)))
    (i32.store (i32.const 0x000B3000) (local.get $h))
    (drop (call $select (local.get $h)))
    (drop (call $invoke (i32.const 0x000B1000) (i32.const ` + ulen + `) (i32.const 0x000B2000) (i32.const 12)))
    (local.get $h)))`
	loadWAT(t, rm, cs, "urn:hdm:test:owner", owner)

	// Intruder: read the leaked handle, try to select it (must be DENIED -> 0), then attempt a
	// get (unpaged, since select failed) which must see nothing. Returns select_ok*1000 + got.
	intruder := `(module
  (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 100))
  (import "hdm:kernel/cell-dispatch" "invoke-cell" (func $invoke (param i32 i32 i32 i32) (result i32)))
  (import "hdm:kernel/paging" "select" (func $select (param i32) (result i32)))
  (func (export "run-tick") (param i32 i32) (result i32)
    (local $h i32) (local $ok i32) (local $got i32)
    (local.set $h (i32.load (i32.const 0x000B3000)))
    (local.set $ok (call $select (local.get $h)))
    (local.set $got (call $invoke (i32.const 0x000B1000) (i32.const ` + ulen + `) (i32.const 0x000B2010) (i32.const 12)))
    (i32.add (i32.mul (local.get $ok) (i32.const 1000)) (local.get $got))))`
	loadWAT(t, rm, cs, "urn:hdm:test:intruder", intruder)

	h, _, _, err := rm.ExecuteTrampoline("h", "urn:hdm:test:owner", "run-tick", 0, 0)
	if err != nil || h == 0 {
		t.Fatalf("owner run: h=%d err=%v", h, err)
	}
	res, _, _, err := rm.ExecuteTrampoline("h", "urn:hdm:test:intruder", "run-tick", 0, 0)
	if err != nil {
		t.Fatalf("intruder run: %v", err)
	}
	// ok must be 0 (select denied) and got must be 0 (never saw the owner's data).
	if res != 0 {
		t.Fatalf("intruder breached ownership: select_ok*1000+got = %d (want 0)", res)
	}

	// Sanity: the data really is there — map the owner's page to the dict (host MapPage is
	// trusted/unenforced) and read key 5 back as 111.
	rm.MapPage(SysDictURN, h)
	got, _, _, err := rm.ExecuteTrampoline("h", SysDictURN, "run-tick", argGet, 12)
	if err != nil || got != 111 {
		t.Fatalf("owner's data unreadable via dict: got=%d err=%v (want 111)", got, err)
	}
}

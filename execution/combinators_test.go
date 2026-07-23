package execution

import (
	"context"
	"encoding/binary"
	"math"
	"path/filepath"
	"testing"

	"github.com/mdcfrancis/flow/compiler"
	"github.com/mdcfrancis/flow/inference"
	"github.com/mdcfrancis/flow/storage"
)

// escapeLeafWAT is the Mandelbrot LEAF: escape-time for ONE point. It reads (cr,ci)
// as two f32 at the arg pointer, iterates z = z*z + c from z=0 up to 64 steps, and
// returns the iteration count before |z|^2 > 4 (64 = in the set). A small imperative
// kernel — the kind the model can get right — applied across the grid by sys:map.
const escapeLeafWAT = `(module
  (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 100))
  (func (export "run-tick") (param $arg i32) (param $len i32) (result i32)
    (local $cr f32) (local $ci f32) (local $zr f32) (local $zi f32)
    (local $zr2 f32) (local $zi2 f32) (local $i i32)
    (local.set $cr (f32.load offset=0 (local.get $arg)))
    (local.set $ci (f32.load offset=4 (local.get $arg)))
    (local.set $zr (f32.const 0)) (local.set $zi (f32.const 0))
    (local.set $i (i32.const 0))
    (block $done
      (loop $loop
        (local.set $zr2 (f32.mul (local.get $zr) (local.get $zr)))
        (local.set $zi2 (f32.mul (local.get $zi) (local.get $zi)))
        (br_if $done (f32.gt (f32.add (local.get $zr2) (local.get $zi2)) (f32.const 4)))
        (local.set $zi (f32.add (f32.mul (f32.const 2) (f32.mul (local.get $zr) (local.get $zi))) (local.get $ci)))
        (local.set $zr (f32.add (f32.sub (local.get $zr2) (local.get $zi2)) (local.get $cr)))
        (local.set $i (i32.add (local.get $i) (i32.const 1)))
        (br_if $done (i32.ge_u (local.get $i) (i32.const 64)))
        (br $loop)))
    (local.get $i)))`

// TestSysMapAppliesLeafAcrossGrid is the P1 proof: a hard numeric kernel becomes a
// tractable imperative LEAF composed with a reusable map. sys:map applies the
// Mandelbrot escape leaf across a grid of points via invoke-cell.
func TestSysMapAppliesLeafAcrossGrid(t *testing.T) {
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
	mapArt, err := cs.CompileGenotype(SysMapWAT)
	if err != nil || !mapArt.SyntaxPassed {
		t.Fatalf("compile sys:map: %v (%s)", err, mapArt.ErrorContext)
	}
	leafArt, err := cs.CompileGenotype(escapeLeafWAT)
	if err != nil || !leafArt.SyntaxPassed {
		t.Fatalf("compile leaf: %v (%s)", err, leafArt.ErrorContext)
	}
	const leafURN = "urn:hdm:test:escape"
	if err := rm.LoadCell(SysMapURN, mapArt.Bytecode); err != nil {
		t.Fatalf("load map: %v", err)
	}
	if err := rm.LoadCell(leafURN, leafArt.Bytecode); err != nil {
		t.Fatalf("load leaf: %v", err)
	}

	// Memory layout (high in the sandbox region, clear of the scratch bump base).
	const urnOff, cfgOff, inOff, outOff = 0xB8000, 0xB9000, 0xBA000, 0xBB000
	wr := func(off uint32, b []byte) {
		if !rm.sharedMem.Write(off, b) {
			t.Fatalf("write at 0x%X failed", off)
		}
	}
	word := func(off uint32, v uint32) {
		var b [4]byte
		binary.LittleEndian.PutUint32(b[:], v)
		wr(off, b[:])
	}
	f32 := func(off uint32, v float32) { word(off, math.Float32bits(v)) }

	wr(urnOff, []byte(leafURN))
	// config: fnPtr, fnLen, inPtr, outPtr, n, elemWords(2: cr,ci)
	word(cfgOff+0, urnOff)
	word(cfgOff+4, uint32(len(leafURN)))
	word(cfgOff+8, inOff)
	word(cfgOff+12, outOff)
	word(cfgOff+16, 4)
	word(cfgOff+20, 2)
	// input grid of c = (cr,ci): in-set, escaping, in-set, far-out
	f32(inOff+0, 0)
	f32(inOff+4, 0) // 0+0i   -> 64 (in set)
	f32(inOff+8, 2)
	f32(inOff+12, 0) // 2+0i   -> 2
	f32(inOff+16, -1)
	f32(inOff+20, 0) // -1+0i  -> 64 (in set)
	f32(inOff+24, 10)
	f32(inOff+28, 10) // 10+10i -> 1

	res, _, _, err := rm.ExecuteTrampoline("urn:hdm:sys:host", SysMapURN, "run-tick", cfgOff, 24)
	if err != nil {
		t.Fatalf("run sys:map: %v", err)
	}
	if res != 4 {
		t.Errorf("map returned %d, want 4 (element count)", res)
	}
	want := []uint32{64, 2, 64, 1}
	for i, w := range want {
		got, ok := rm.PeekU32(uint32(outOff + i*4))
		if !ok {
			t.Fatalf("read out[%d] failed", i)
		}
		if got != w {
			t.Errorf("escape[%d] = %d, want %d", i, got, w)
		}
	}
}

package execution

import (
	"context"
	"encoding/binary"
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/mdcfrancis/flow/compiler"
	"github.com/mdcfrancis/flow/inference"
	"github.com/mdcfrancis/flow/storage"
)

// leaf functions for the combinator tests (ABI: run-tick(argPtr,argLen)->i32).
const (
	// addAcc: [acc, x] -> acc+x  (fold/scan/zip)
	leafAdd = `(module (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 100))
      (func (export "run-tick") (param $a i32) (param $l i32) (result i32)
        (i32.add (i32.load offset=0 (local.get $a)) (i32.load offset=4 (local.get $a)))))`
	// isPos: [x] -> x>0
	leafPos = `(module (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 100))
      (func (export "run-tick") (param $a i32) (param $l i32) (result i32)
        (i32.gt_s (i32.load (local.get $a)) (i32.const 0))))`
	// dec: state[0]-- ; return state[0]>0
	leafDec = `(module (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 100))
      (func (export "run-tick") (param $a i32) (param $l i32) (result i32)
        (i32.store (local.get $a) (i32.sub (i32.load (local.get $a)) (i32.const 1)))
        (i32.gt_s (i32.load (local.get $a)) (i32.const 0))))`
)

func loadWAT(t *testing.T, rm *RuntimeManager, cs *compiler.CompilerService, urn, wat string) {
	art, err := cs.CompileGenotype(wat)
	if err != nil || !art.SyntaxPassed {
		t.Fatalf("compile %s: %v (%s)", urn, err, art.ErrorContext)
	}
	if err := rm.LoadCell(urn, art.Bytecode); err != nil {
		t.Fatalf("load %s: %v", urn, err)
	}
}

func TestSystemCombinators(t *testing.T) {
	ctx := context.Background()
	le, _ := storage.NewLedgerEngine(filepath.Join(t.TempDir(), "hdm.db"))
	defer le.Close()
	rm, err := NewRuntimeManager(ctx, le, inference.NewLocalModelClient("http://127.0.0.1:0", "test"))
	if err != nil {
		t.Fatalf("runtime: %v", err)
	}
	defer rm.Close(ctx)
	cs := compiler.NewCompilerService()
	for _, c := range SystemCombinators() {
		loadWAT(t, rm, cs, c.URN, c.WAT)
	}
	loadWAT(t, rm, cs, "urn:hdm:test:add", leafAdd)
	loadWAT(t, rm, cs, "urn:hdm:test:pos", leafPos)
	loadWAT(t, rm, cs, "urn:hdm:test:dec", leafDec)

	w := func(off uint32, v uint32) {
		var b [4]byte
		binary.LittleEndian.PutUint32(b[:], v)
		rm.sharedMem.Write(off, b[:])
	}
	r := func(off uint32) uint32 { v, _ := rm.PeekU32(off); return v }
	putURN := func(off uint32, s string) (uint32, uint32) {
		rm.sharedMem.Write(off, []byte(s))
		return off, uint32(len(s))
	}

	// region bases (non-overlapping)
	const U, CFG, IN, OUT, ARG, ST = 0xB8000, 0xB8100, 0xB8200, 0xB8300, 0xB8400, 0xB8500

	t.Run("fold_sum", func(t *testing.T) {
		fp, fl := putURN(U, "urn:hdm:test:add")
		for i, v := range []uint32{1, 2, 3, 4} {
			w(IN+uint32(i*4), v)
		}
		// cfg: fnPtr,fnLen,inPtr,n,elemWords,seed,argBufPtr
		w(CFG+0, fp)
		w(CFG+4, fl)
		w(CFG+8, IN)
		w(CFG+12, 4)
		w(CFG+16, 1)
		w(CFG+20, 0)
		w(CFG+24, ARG)
		res, _, _, err := rm.ExecuteTrampoline("h", SysFoldURN, "run-tick", CFG, 28)
		if err != nil || res != 10 {
			t.Fatalf("fold sum = %d (err %v), want 10", res, err)
		}
	})

	t.Run("filter_positive", func(t *testing.T) {
		fp, fl := putURN(U, "urn:hdm:test:pos")
		vals := []int32{-1, 2, -3, 4}
		for i, v := range vals {
			w(IN+uint32(i*4), uint32(v))
		}
		w(CFG+0, fp)
		w(CFG+4, fl)
		w(CFG+8, IN)
		w(CFG+12, OUT)
		w(CFG+16, 4)
		w(CFG+20, 1)
		res, _, _, err := rm.ExecuteTrampoline("h", SysFilterURN, "run-tick", CFG, 24)
		if err != nil || res != 2 {
			t.Fatalf("filter kept = %d (err %v), want 2", res, err)
		}
		if r(OUT) != 2 || r(OUT+4) != 4 {
			t.Errorf("filter out = [%d,%d], want [2,4]", r(OUT), r(OUT+4))
		}
	})

	t.Run("iterate_until_dec", func(t *testing.T) {
		fp, fl := putURN(U, "urn:hdm:test:dec")
		w(ST, 5) // state = 5
		w(CFG+0, fp)
		w(CFG+4, fl)
		w(CFG+8, ST)
		w(CFG+12, 1)
		w(CFG+16, 100)
		res, _, _, err := rm.ExecuteTrampoline("h", SysIterateURN, "run-tick", CFG, 20)
		if err != nil || res != 5 {
			t.Fatalf("iterate steps = %d (err %v), want 5", res, err)
		}
		if r(ST) != 0 {
			t.Errorf("state after = %d, want 0", r(ST))
		}
	})

	t.Run("scan_prefix_sum", func(t *testing.T) {
		fp, fl := putURN(U, "urn:hdm:test:add")
		for i, v := range []uint32{1, 2, 3, 4} {
			w(IN+uint32(i*4), v)
		}
		w(CFG+0, fp)
		w(CFG+4, fl)
		w(CFG+8, IN)
		w(CFG+12, OUT)
		w(CFG+16, 4)
		w(CFG+20, 1)
		w(CFG+24, 0)
		w(CFG+28, ARG)
		res, _, _, err := rm.ExecuteTrampoline("h", SysScanURN, "run-tick", CFG, 32)
		if err != nil || res != 4 {
			t.Fatalf("scan = %d (err %v), want 4", res, err)
		}
		want := []uint32{1, 3, 6, 10}
		for i, x := range want {
			if r(OUT+uint32(i*4)) != x {
				t.Errorf("scan out[%d] = %d, want %d", i, r(OUT+uint32(i*4)), x)
			}
		}
	})

	t.Run("zip_add", func(t *testing.T) {
		fp, fl := putURN(U, "urn:hdm:test:add")
		a := []uint32{1, 2, 3}
		b := []uint32{10, 20, 30}
		for i := range a {
			w(IN+uint32(i*4), a[i])
			w(0xB8280+uint32(i*4), b[i]) // B array
		}
		// cfg: fnPtr,fnLen,aPtr,bPtr,outPtr,n,elemWords,argBufPtr
		w(CFG+0, fp)
		w(CFG+4, fl)
		w(CFG+8, IN)
		w(CFG+12, 0xB8280)
		w(CFG+16, OUT)
		w(CFG+20, 3)
		w(CFG+24, 1)
		w(CFG+28, ARG)
		res, _, _, err := rm.ExecuteTrampoline("h", SysZipURN, "run-tick", CFG, 32)
		if err != nil || res != 3 {
			t.Fatalf("zip = %d (err %v), want 3", res, err)
		}
		want := []uint32{11, 22, 33}
		for i, x := range want {
			if r(OUT+uint32(i*4)) != x {
				t.Errorf("zip out[%d] = %d, want %d", i, r(OUT+uint32(i*4)), x)
			}
		}
	})
}

// TestGenerateMapDriver proves HDM can GENERATE the composition boilerplate: a
// generated driver cell runs map(escape_leaf) over the grid with no hand-written
// wiring — the model would only supply the leaf.
func TestGenerateMapDriver(t *testing.T) {
	ctx := context.Background()
	le, _ := storage.NewLedgerEngine(filepath.Join(t.TempDir(), "hdm.db"))
	defer le.Close()
	rm, err := NewRuntimeManager(ctx, le, inference.NewLocalModelClient("http://127.0.0.1:0", "test"))
	if err != nil {
		t.Fatalf("runtime: %v", err)
	}
	defer rm.Close(ctx)
	cs := compiler.NewCompilerService()

	const inOff, outOff = 0xB2000, 0xB3000
	loadWAT(t, rm, cs, SysMapURN, SysMapWAT)
	loadWAT(t, rm, cs, "urn:hdm:test:escape", escapeLeafWAT)
	driver := GenerateMapDriver("urn:hdm:test:escape", inOff, outOff, 4, 2)
	loadWAT(t, rm, cs, "urn:hdm:test:mandeldriver", driver)

	f32 := func(off uint32, v float32) {
		var b [4]byte
		binary.LittleEndian.PutUint32(b[:], math.Float32bits(v))
		rm.sharedMem.Write(off, b[:])
	}
	f32(inOff+0, 0)
	f32(inOff+4, 0)
	f32(inOff+8, 2)
	f32(inOff+12, 0)
	f32(inOff+16, -1)
	f32(inOff+20, 0)
	f32(inOff+24, 10)
	f32(inOff+28, 10)

	if _, _, _, err := rm.ExecuteTrampoline("h", "urn:hdm:test:mandeldriver", "run-tick", 0, 0); err != nil {
		t.Fatalf("run generated driver: %v", err)
	}
	want := []uint32{64, 2, 64, 1}
	for i, x := range want {
		if got, _ := rm.PeekU32(uint32(outOff + i*4)); got != x {
			t.Errorf("driver escape[%d] = %d, want %d", i, got, x)
		}
	}
}

// TestFusionMapAtScale: with instance caching, mapping the escape leaf over a large
// grid completes fast (one instantiation reused across thousands of calls).
func TestFusionMapAtScale(t *testing.T) {
	ctx := context.Background()
	le, _ := storage.NewLedgerEngine(filepath.Join(t.TempDir(), "hdm.db"))
	defer le.Close()
	rm, err := NewRuntimeManager(ctx, le, inference.NewLocalModelClient("http://127.0.0.1:0", "test"))
	if err != nil {
		t.Fatalf("runtime: %v", err)
	}
	defer rm.Close(ctx)
	cs := compiler.NewCompilerService()
	loadWAT(t, rm, cs, SysMapURN, SysMapWAT)
	loadWAT(t, rm, cs, "urn:hdm:test:escape", escapeLeafWAT)

	const N = 3072
	const inOff, outOff = 0xB0000, 0xC0000 // in the shared region
	driver := GenerateMapDriver("urn:hdm:test:escape", inOff, outOff, N, 2)
	loadWAT(t, rm, cs, "urn:hdm:test:driver", driver)
	// seed all points to c=0 (in the set -> escape 64)
	for i := 0; i < N*2; i++ {
		var b [4]byte
		binary.LittleEndian.PutUint32(b[:], 0)
		rm.sharedMem.Write(uint32(inOff+i*4), b[:])
	}
	start := time.Now()
	if _, _, _, err := rm.ExecuteTrampoline("h", "urn:hdm:test:driver", "run-tick", 0, 0); err != nil {
		t.Fatalf("run driver at scale: %v", err)
	}
	dt := time.Since(start)
	// spot-check a few outputs = 64 (in set)
	for _, i := range []int{0, 1000, N - 1} {
		if v, _ := rm.PeekU32(uint32(outOff + i*4)); v != 64 {
			t.Errorf("escape[%d] = %d, want 64", i, v)
		}
	}
	t.Logf("mapped %d points in %v (fused: one leaf instance reused)", N, dt)
}

// TestMemoizationCorrectAndHits: a second map over the SAME input is all cache hits
// (the pure leaf is memoized), and the memoized outputs equal the first pass.
func TestMemoizationCorrectAndHits(t *testing.T) {
	ctx := context.Background()
	le, _ := storage.NewLedgerEngine(filepath.Join(t.TempDir(), "hdm.db"))
	defer le.Close()
	rm, err := NewRuntimeManager(ctx, le, inference.NewLocalModelClient("http://127.0.0.1:0", "test"))
	if err != nil {
		t.Fatalf("runtime: %v", err)
	}
	defer rm.Close(ctx)
	cs := compiler.NewCompilerService()
	loadWAT(t, rm, cs, SysMapURN, SysMapWAT)
	loadWAT(t, rm, cs, "urn:hdm:test:escape", escapeLeafWAT) // pure: zero host-fn imports

	const N = 512
	const inOff, outOff = 0xB0000, 0xC0000
	driver := GenerateMapDriver("urn:hdm:test:escape", inOff, outOff, N, 2)
	loadWAT(t, rm, cs, "urn:hdm:test:driver", driver)
	// distinct points: cr varies, ci=0 -> a spread of escape counts
	for i := 0; i < N; i++ {
		cr := math.Float32bits(float32(-2.0) + float32(i)*4.0/float32(N))
		var b [8]byte
		binary.LittleEndian.PutUint32(b[0:], cr)
		binary.LittleEndian.PutUint32(b[4:], 0)
		rm.sharedMem.Write(uint32(inOff+i*8), b[:])
	}
	run := func() []uint32 {
		if _, _, _, err := rm.ExecuteTrampoline("h", "urn:hdm:test:driver", "run-tick", 0, 0); err != nil {
			t.Fatalf("run: %v", err)
		}
		out := make([]uint32, N)
		for i := 0; i < N; i++ {
			out[i], _ = rm.PeekU32(uint32(outOff + i*4))
		}
		return out
	}
	first := run()
	h0, m0 := rm.MemoStats()
	if m0 < N {
		t.Fatalf("first pass should MISS every distinct point: misses=%d want>=%d", m0, N)
	}
	// wipe outputs so the second pass must repopulate from the memo, not stale memory
	for i := 0; i < N; i++ {
		var z [4]byte
		rm.sharedMem.Write(uint32(outOff+i*4), z[:])
	}
	second := run()
	h1, _ := rm.MemoStats()
	if h1-h0 < N {
		t.Fatalf("second pass over same input should be all HITS: hits gained=%d want>=%d", h1-h0, N)
	}
	for i := 0; i < N; i++ {
		if first[i] != second[i] {
			t.Fatalf("memoized result diverged at %d: %d vs %d", i, first[i], second[i])
		}
	}
	t.Logf("N=%d: pass1 misses=%d, pass2 hits=%d — memo correct + effective", N, m0, h1-h0)
}

// TestCellIsDeterministic: a pure leaf (zero host imports) is deterministic; a cell
// importing chronos is not (so it is never memo-skipped).
func TestCellIsDeterministic(t *testing.T) {
	ctx := context.Background()
	le, _ := storage.NewLedgerEngine(filepath.Join(t.TempDir(), "hdm.db"))
	defer le.Close()
	rm, err := NewRuntimeManager(ctx, le, inference.NewLocalModelClient("http://127.0.0.1:0", "test"))
	if err != nil {
		t.Fatalf("runtime: %v", err)
	}
	defer rm.Close(ctx)
	cs := compiler.NewCompilerService()
	pure := compileWAT(t, cs, escapeLeafWAT)
	if !rm.CellIsDeterministic("h-pure", pure) {
		t.Fatal("pure leaf should be deterministic")
	}
	clockWAT := `(module
  (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 4))
  (import "hdm:kernel/chronos" "tick" (func $tick (result i32)))
  (func (export "run-tick") (param i32 i32) (result i32) (call $tick)))`
	clock := compileWAT(t, cs, clockWAT)
	if rm.CellIsDeterministic("h-clock", clock) {
		t.Fatal("a chronos-importing cell must NOT be deterministic")
	}
}

func compileWAT(t *testing.T, cs *compiler.CompilerService, wat string) []byte {
	t.Helper()
	art, err := cs.CompileGenotype(wat)
	if err != nil || !art.SyntaxPassed {
		t.Fatalf("compile: %v (%s)", err, art.ErrorContext)
	}
	return art.Bytecode
}

// TestTickAppCellMasked: a cell's write outside its write-mask is reverted, a read of a
// poisoned field sees 0, and the poisoned field is restored afterward.
func TestTickAppCellMasked(t *testing.T) {
	ctx := context.Background()
	le, _ := storage.NewLedgerEngine(filepath.Join(t.TempDir(), "hdm.db"))
	defer le.Close()
	rm, err := NewRuntimeManager(ctx, le, inference.NewLocalModelClient("http://127.0.0.1:0", "test"))
	if err != nil {
		t.Fatalf("runtime: %v", err)
	}
	defer rm.Close(ctx)
	cs := compiler.NewCompilerService()
	wat := `(module
  (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 16))
  (func (export "run-tick") (param i32 i32) (result i32)
    (i32.store (i32.const 0xB0000) (i32.const 111))
    (i32.store (i32.const 0xB0100) (i32.const 222))
    (i32.load (i32.const 0xB0200))))`
	loadWAT(t, rm, cs, "urn:hdm:test:masked", wat)
	seed := func(off, v uint32) {
		var b [4]byte
		binary.LittleEndian.PutUint32(b[:], v)
		rm.sharedMem.Write(off, b[:])
	}
	seed(0xB0000, 0)
	seed(0xB0100, 5)   // pre-existing value the cell must NOT be able to overwrite
	seed(0xB0200, 999) // a field the cell may NOT read (poisoned to 0 during the tick)
	// writable: 0xB0000 only. non-writable (revert): 0xB0100, 0xB0200. non-readable: 0xB0200.
	rm.SetMasksEnabled(true)
	rm.SetCellMask("urn:hdm:test:masked",
		[][2]uint32{{0xB0200, 4}},               // poison
		[][2]uint32{{0xB0100, 4}, {0xB0200, 4}}) // revert
	res, _, _, err := rm.ExecuteTrampoline("h", "urn:hdm:test:masked", "run-tick", 0, 0)
	if err != nil {
		t.Fatalf("masked tick: %v", err)
	}
	if res != 0 {
		t.Errorf("poisoned read should see 0, got %d", res)
	}
	if v, _ := rm.PeekU32(0xB0000); v != 111 {
		t.Errorf("allowed write should persist: 0xB0000=%d want 111", v)
	}
	if v, _ := rm.PeekU32(0xB0100); v != 5 {
		t.Errorf("disallowed write must be reverted: 0xB0100=%d want 5", v)
	}
	if v, _ := rm.PeekU32(0xB0200); v != 999 {
		t.Errorf("poisoned field must be restored: 0xB0200=%d want 999", v)
	}
}

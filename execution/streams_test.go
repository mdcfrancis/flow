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

// stream test leaves.
const (
	// range: state=[value,current,limit]; yields current, cur++, until cur>=limit.
	leafRange = `(module (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 100))
      (func (export "run-tick") (param $s i32) (param $l i32) (result i32)
        (local $cur i32) (local $lim i32)
        (local.set $cur (i32.load offset=4 (local.get $s)))
        (local.set $lim (i32.load offset=8 (local.get $s)))
        (if (result i32) (i32.ge_s (local.get $cur) (local.get $lim))
          (then (i32.const 0))
          (else
            (i32.store offset=0 (local.get $s) (local.get $cur))
            (i32.store offset=4 (local.get $s) (i32.add (local.get $cur) (i32.const 1)))
            (i32.const 1)))))`
	leafLt5 = `(module (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 100))
      (func (export "run-tick") (param $a i32) (param $l i32) (result i32) (i32.lt_s (i32.load (local.get $a)) (i32.const 5))))`
	leafLt25 = `(module (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 100))
      (func (export "run-tick") (param $a i32) (param $l i32) (result i32) (i32.lt_s (i32.load (local.get $a)) (i32.const 25))))`
	leafAdd10 = `(module (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 100))
      (func (export "run-tick") (param $a i32) (param $l i32) (result i32) (i32.add (i32.load (local.get $a)) (i32.const 10))))`
)

func TestLazyStreams(t *testing.T) {
	ctx := context.Background()
	le, _ := storage.NewLedgerEngine(filepath.Join(t.TempDir(), "hdm.db"))
	defer le.Close()
	rm, err := NewRuntimeManager(ctx, le, inference.NewLocalModelClient("http://127.0.0.1:0", "test"))
	if err != nil {
		t.Fatalf("runtime: %v", err)
	}
	defer rm.Close(ctx)
	cs := compiler.NewCompilerService()
	for _, c := range StreamCombinators() {
		loadWAT(t, rm, cs, c.URN, c.WAT)
	}
	loadWAT(t, rm, cs, "urn:hdm:test:range", leafRange)
	loadWAT(t, rm, cs, "urn:hdm:test:lt5", leafLt5)
	loadWAT(t, rm, cs, "urn:hdm:test:lt25", leafLt25)
	loadWAT(t, rm, cs, "urn:hdm:test:add", leafAdd) // from combinators_lib_test.go
	loadWAT(t, rm, cs, "urn:hdm:test:add10", leafAdd10)

	w := func(off uint32, v uint32) {
		var b [4]byte
		binary.LittleEndian.PutUint32(b[:], v)
		rm.sharedMem.Write(off, b[:])
	}
	// bump-allocate URN strings, returning (ptr,len)
	next := uint32(0xB8000)
	urn := func(s string) (uint32, uint32) {
		p := next
		rm.sharedMem.Write(p, []byte(s))
		next += uint32(len(s)+3) &^ 3
		return p, uint32(len(s))
	}
	rp, rl := urn("urn:hdm:test:range")
	mp, ml := urn(SysStreamMapURN)
	ap, al := urn("urn:hdm:test:add")
	a10p, a10l := urn("urn:hdm:test:add10")
	p5, l5 := urn("urn:hdm:test:lt5")
	p25, l25 := urn("urn:hdm:test:lt25")

	const ST, CFG, ARG, MST = 0xB9000, 0xB9100, 0xB9200, 0xB9300

	t.Run("fold_sum_range", func(t *testing.T) {
		w(ST+0, 0)
		w(ST+4, 0)
		w(ST+8, 10) // range [0,10)
		// cfg: stepPtr,stepLen,statePtr,stateBytes,fnPtr,fnLen,seed,argBufPtr,max
		w(CFG+0, rp)
		w(CFG+4, rl)
		w(CFG+8, ST)
		w(CFG+12, 12)
		w(CFG+16, ap)
		w(CFG+20, al)
		w(CFG+24, 0)
		w(CFG+28, ARG)
		w(CFG+32, 1000)
		res, _, _, err := rm.ExecuteTrampoline("h", SysStreamFoldURN, "run-tick", CFG, 36)
		if err != nil || res != 45 {
			t.Fatalf("fold sum = %d (err %v), want 45", res, err)
		}
	})

	t.Run("take_while_is_lazy", func(t *testing.T) {
		w(ST+0, 0)
		w(ST+4, 0)
		w(ST+8, 100) // range [0,100)
		// cfg: stepPtr,stepLen,statePtr,stateBytes,predPtr,predLen,max
		w(CFG+0, rp)
		w(CFG+4, rl)
		w(CFG+8, ST)
		w(CFG+12, 12)
		w(CFG+16, p5)
		w(CFG+20, l5)
		w(CFG+24, 1000)
		res, _, _, err := rm.ExecuteTrampoline("h", SysStreamTakeWhileURN, "run-tick", CFG, 28)
		if err != nil || res != 5 {
			t.Fatalf("take_while(<5) = %d (err %v), want 5", res, err)
		}
		// LAZINESS: the range advanced only to 6, not 100 — values past the stop
		// point were never computed.
		if cur, _ := rm.PeekU32(ST + 4); cur != 6 {
			t.Errorf("range current = %d, want 6 (lazy: stopped early, did not run to 100)", cur)
		}
	})

	t.Run("lazy_pipeline_map_takewhile", func(t *testing.T) {
		// source range [0,100)
		w(ST+0, 0)
		w(ST+4, 0)
		w(ST+8, 100)
		// map state: {value, srcStatePtr, srcStepPtr, srcStepLen, fnPtr, fnLen, srcStateBytes}
		w(MST+0, 0)
		w(MST+4, ST)
		w(MST+8, rp)
		w(MST+12, rl)
		w(MST+16, a10p)
		w(MST+20, a10l)
		w(MST+24, 12)
		// take_while(<25) over the MAP stream (step = stream-map, state = MST)
		w(CFG+0, mp)
		w(CFG+4, ml)
		w(CFG+8, MST)
		w(CFG+12, 28)
		w(CFG+16, p25)
		w(CFG+20, l25)
		w(CFG+24, 1000)
		res, _, _, err := rm.ExecuteTrampoline("h", SysStreamTakeWhileURN, "run-tick", CFG, 28)
		if err != nil || res != 15 {
			t.Fatalf("take_while(<25, map(+10, range)) = %d (err %v), want 15", res, err)
		}
		// laziness through the pipeline: source advanced only ~16, not 100.
		if cur, _ := rm.PeekU32(ST + 4); cur > 20 {
			t.Errorf("source advanced to %d — pipeline not lazy (should stop near 16)", cur)
		}
	})
}

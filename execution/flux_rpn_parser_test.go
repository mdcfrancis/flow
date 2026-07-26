package execution

import (
	"context"
	"encoding/binary"
	"path/filepath"
	"testing"

	"github.com/mdcfrancis/flow/compiler"
	"github.com/mdcfrancis/flow/flux"
	"github.com/mdcfrancis/flow/inference"
	"github.com/mdcfrancis/flow/storage"
)

// rpnReduceCell is the SHIFT-REDUCE engine of the parser, authored entirely in Flux
// over the buffer capability: each tick it reads the token at the cursor and either
// SHIFTS it onto the stack (a number, token >= 0) or REDUCES (an operator, token < 0:
// pop two, apply, push the result). Driven over the token stream it evaluates any
// well-formed RPN expression — the classic shift-reduce parse. Division is guarded so
// a 0 divisor (present in unused stack slots, and evaluated by select's both-branch
// semantics) can never trap.
const rpnReduceCell = `(cell rpn (reads tokens cursor stack sp) (writes stack sp cursor)
  (let ([tok (at tokens cursor)]
        [isop (< tok 0)]
        [op (neg tok)]
        [b (at stack (- sp 1))]
        [a (at stack (- sp 2))]
        [safeb (if (= b 0) 1 b)]
        [applied (if (= op 1) (+ a b) (if (= op 2) (- a b) (if (= op 3) (* a b) (/ a safeb))))]
        [sidx (if isop (- sp 2) sp)]
        [sval (if isop applied tok)])
    (write
      (store stack sidx sval)
      (sp (if isop (- sp 1) (+ sp 1)))
      (cursor (+ cursor 1)))))`

func TestFluxRPNShiftReduceParser(t *testing.T) {
	ctx := context.Background()
	le, _ := storage.NewLedgerEngine(filepath.Join(t.TempDir(), "hdm.db"))
	defer le.Close()
	rm, err := NewRuntimeManager(ctx, le, inference.NewLocalModelClient("http://127.0.0.1:0", "test"))
	if err != nil {
		t.Fatalf("runtime: %v", err)
	}
	defer rm.Close(ctx)
	cs := compiler.NewCompilerService()

	const tokBase, stkBase, cursorOff, spOff = 0xB2000, 0xB3000, 0xB0000, 0xB0004
	const cap = 16
	layout := flux.Layout{
		"tokens": {Type: flux.TBuffer, Offset: tokBase, Len: cap},
		"stack":  {Type: flux.TBuffer, Offset: stkBase, Len: cap},
		"cursor": {Type: flux.TInt, Offset: cursorOff},
		"sp":     {Type: flux.TInt, Offset: spOff},
	}
	wat, err := flux.Compile("m", rpnReduceCell, layout)
	if err != nil {
		t.Fatalf("flux compile: %v", err)
	}
	loadWAT(t, rm, cs, "urn:hdm:test:rpn", wat)

	w := func(off uint32, v int32) {
		var b [4]byte
		binary.LittleEndian.PutUint32(b[:], uint32(v))
		rm.sharedMem.Write(off, b[:])
	}
	r := func(off uint32) int32 { v, _ := rm.PeekU32(off); return int32(v) }

	// operators encoded as negatives: -1=+ -2=- -3=* -4=/
	const add, sub, mul, div = -1, -2, -3, -4
	eval := func(toks []int32) int32 {
		for i := 0; i < cap; i++ {
			w(tokBase+uint32(i*4), 0)
			w(stkBase+uint32(i*4), 0)
		}
		for i, tk := range toks {
			w(tokBase+uint32(i*4), tk)
		}
		w(cursorOff, 0)
		w(spOff, 0)
		for range toks {
			if _, _, _, err := rm.ExecuteTrampoline("h", "urn:hdm:test:rpn", "run-tick", 0, 0); err != nil {
				t.Fatalf("run: %v", err)
			}
		}
		return r(stkBase) // result is stack[0]
	}

	cases := []struct {
		name string
		toks []int32
		want int32
	}{
		{"3 4 +", []int32{3, 4, add}, 7},
		{"(3+4)*2", []int32{3, 4, add, 2, mul}, 14},
		{"8 2 /", []int32{8, 2, div}, 4},
		{"5 + (1+2)*4 - 3", []int32{5, 1, 2, add, 4, mul, add, 3, sub}, 14},
		{"20 4 3 - /", []int32{20, 4, 3, sub, div}, 20}, // 20/(4-3)=20
	}
	for _, c := range cases {
		if got := eval(c.toks); got != c.want {
			t.Errorf("%s = %d, want %d", c.name, got, c.want)
		}
	}
}

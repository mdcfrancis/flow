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

// mlexCell is the MULTI-DIGIT tokenizer: it accumulates digit runs into a number
// (acc), and on a boundary (operator, space, or end-of-input) FLUSHES the number as a
// token, then — if the boundary is an operator — also emits the op code. A tick can
// thus emit up to two tokens (flush + op), at outp and outp+1; both stores are
// self-preserving so a no-op tick leaves the buffer untouched. innum tracks whether a
// digit run is open. A trailing non-digit byte (the zero past the input) flushes the
// final number. This makes the parser handle real numbers, not just single digits.
const mlexCell = `(cell mlex (reads src cursor tokens outp acc innum) (writes tokens outp cursor acc innum)
  (let ([byte (at src cursor)]
        [isdigit (and (>= byte 48) (<= byte 57))]
        [isop (or (or (= byte 43) (= byte 45)) (or (= byte 42) (= byte 47)))]
        [flushNum (and (> innum 0) (not isdigit))]
        [opcode (if (= byte 43) -1 (if (= byte 45) -2 (if (= byte 42) -3 -4)))]
        [opPos (+ outp (if flushNum 1 0))]
        [keepNum (at tokens outp)]
        [keepOp (at tokens opPos)])
    (write
      (store tokens outp (if flushNum acc keepNum))
      (store tokens opPos (if isop opcode keepOp))
      (outp (+ outp (+ (if flushNum 1 0) (if isop 1 0))))
      (acc (if isdigit (+ (* acc 10) (- byte 48)) 0))
      (innum (if isdigit 1 0))
      (cursor (+ cursor 1)))))`

// TestFluxMultiDigitParser is the full parser with a real lexer: multi-digit numbers
// through the same shift-reduce engine. Both stages Flux, over the buffer capability.
func TestFluxMultiDigitParser(t *testing.T) {
	ctx := context.Background()
	le, _ := storage.NewLedgerEngine(filepath.Join(t.TempDir(), "hdm.db"))
	defer le.Close()
	rm, err := NewRuntimeManager(ctx, le, inference.NewLocalModelClient("http://127.0.0.1:0", "test"))
	if err != nil {
		t.Fatalf("runtime: %v", err)
	}
	defer rm.Close(ctx)
	cs := compiler.NewCompilerService()

	const srcBase, tokBase, stkBase = 0xB1000, 0xB2000, 0xB3000
	const lexCur, outp, acc, innum, rpnCur, sp = 0xB0000, 0xB0004, 0xB0008, 0xB000C, 0xB0010, 0xB0014
	const cap = 32

	lexLayout := flux.Layout{
		"src":    {Type: flux.TBuffer, Offset: srcBase, Len: cap},
		"tokens": {Type: flux.TBuffer, Offset: tokBase, Len: cap},
		"cursor": {Type: flux.TInt, Offset: lexCur},
		"outp":   {Type: flux.TInt, Offset: outp},
		"acc":    {Type: flux.TInt, Offset: acc},
		"innum":  {Type: flux.TInt, Offset: innum},
	}
	rpnLayout := flux.Layout{
		"tokens": {Type: flux.TBuffer, Offset: tokBase, Len: cap},
		"stack":  {Type: flux.TBuffer, Offset: stkBase, Len: cap},
		"cursor": {Type: flux.TInt, Offset: rpnCur},
		"sp":     {Type: flux.TInt, Offset: sp},
	}
	lexWAT, err := flux.Compile("m", mlexCell, lexLayout)
	if err != nil {
		t.Fatalf("mlex compile: %v", err)
	}
	rpnWAT, err := flux.Compile("m", rpnReduceCell, rpnLayout)
	if err != nil {
		t.Fatalf("rpn compile: %v", err)
	}
	loadWAT(t, rm, cs, "urn:hdm:test:mlex", lexWAT)
	loadWAT(t, rm, cs, "urn:hdm:test:rpn3", rpnWAT)

	w := func(off uint32, v int32) {
		var b [4]byte
		binary.LittleEndian.PutUint32(b[:], uint32(v))
		rm.sharedMem.Write(off, b[:])
	}
	r := func(off uint32) int32 { v, _ := rm.PeekU32(off); return int32(v) }

	parse := func(text string) int32 {
		for i := 0; i < cap; i++ {
			w(srcBase+uint32(i*4), 0)
			w(tokBase+uint32(i*4), 0)
			w(stkBase+uint32(i*4), 0)
		}
		for i, ch := range []byte(text) {
			w(srcBase+uint32(i*4), int32(ch))
		}
		w(lexCur, 0)
		w(outp, 0)
		w(acc, 0)
		w(innum, 0)
		// len+1 ticks: the extra tick reads the zero past the input and flushes the
		// final number.
		for i := 0; i <= len(text); i++ {
			if _, _, _, err := rm.ExecuteTrampoline("h", "urn:hdm:test:mlex", "run-tick", 0, 0); err != nil {
				t.Fatalf("mlex: %v", err)
			}
		}
		nTokens := int(r(outp))
		w(rpnCur, 0)
		w(sp, 0)
		for i := 0; i < nTokens; i++ {
			if _, _, _, err := rm.ExecuteTrampoline("h", "urn:hdm:test:rpn3", "run-tick", 0, 0); err != nil {
				t.Fatalf("rpn: %v", err)
			}
		}
		return r(stkBase)
	}

	cases := []struct {
		text string
		want int32
	}{
		{"12 34 +", 46},
		{"100 5 /", 20},
		{"12 3 4 * +", 24},    // 12 + (3*4)
		{"200 50 - 3 *", 450}, // (200-50)*3
		{"42 8 -", 34},        // multi-digit subtraction
		{"1000 1 +", 1001},
	}
	for _, c := range cases {
		if got := parse(c.text); got != c.want {
			t.Errorf("parse(%q) = %d, want %d", c.text, got, c.want)
		}
	}
}

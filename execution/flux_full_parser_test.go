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

// lexCell is the TOKENIZER stage, authored in Flux over the buffer capability: each
// tick it reads one input byte and, for a single-digit operand (0-9) or an operator
// (+ - * /), emits a token (the digit's value, or a negative op code) into the token
// buffer, advancing the output cursor; spaces and anything else are skipped. Skipping
// preserves the current output slot by storing its own value back (Flux stores are
// unconditional). Over the input it produces the token stream the reduce engine consumes.
const lexCell = `(cell lex (reads src cursor tokens outp) (writes tokens outp cursor)
  (let ([byte (at src cursor)]
        [isdigit (and (>= byte 48) (<= byte 57))]
        [isop (or (or (= byte 43) (= byte 45)) (or (= byte 42) (= byte 47)))]
        [emit (or isdigit isop)]
        [opcode (if (= byte 43) -1 (if (= byte 45) -2 (if (= byte 42) -3 -4)))]
        [tokenval (if isdigit (- byte 48) opcode)]
        [keep (at tokens outp)])
    (write
      (store tokens outp (if emit tokenval keep))
      (outp (if emit (+ outp 1) outp))
      (cursor (+ cursor 1)))))`

// TestFluxFullParser composes the two Flux stages into a COMPLETE parser: raw text
// bytes → (lex) → token stream → (rpn shift-reduce) → result. Both stages are Flux
// cells over the buffer capability; no hand-WAT anywhere. This is a full parser for
// single-digit RPN arithmetic (all four operators, spaces, arbitrary nesting).
func TestFluxFullParser(t *testing.T) {
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
	const lexCur, outp, rpnCur, sp = 0xB0000, 0xB0004, 0xB0008, 0xB000C
	const cap = 32

	lexLayout := flux.Layout{
		"src":    {Type: flux.TBuffer, Offset: srcBase, Len: cap},
		"tokens": {Type: flux.TBuffer, Offset: tokBase, Len: cap},
		"cursor": {Type: flux.TInt, Offset: lexCur},
		"outp":   {Type: flux.TInt, Offset: outp},
	}
	rpnLayout := flux.Layout{
		"tokens": {Type: flux.TBuffer, Offset: tokBase, Len: cap},
		"stack":  {Type: flux.TBuffer, Offset: stkBase, Len: cap},
		"cursor": {Type: flux.TInt, Offset: rpnCur},
		"sp":     {Type: flux.TInt, Offset: sp},
	}
	lexWAT, err := flux.Compile("m", lexCell, lexLayout)
	if err != nil {
		t.Fatalf("lex compile: %v", err)
	}
	rpnWAT, err := flux.Compile("m", rpnReduceCell, rpnLayout) // from flux_rpn_parser_test.go
	if err != nil {
		t.Fatalf("rpn compile: %v", err)
	}
	loadWAT(t, rm, cs, "urn:hdm:test:lex", lexWAT)
	loadWAT(t, rm, cs, "urn:hdm:test:rpn2", rpnWAT)

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
		// Stage 1: tokenize the whole input.
		w(lexCur, 0)
		w(outp, 0)
		for range text {
			if _, _, _, err := rm.ExecuteTrampoline("h", "urn:hdm:test:lex", "run-tick", 0, 0); err != nil {
				t.Fatalf("lex: %v", err)
			}
		}
		nTokens := int(r(outp))
		// Stage 2: shift-reduce the token stream.
		w(rpnCur, 0)
		w(sp, 0)
		for i := 0; i < nTokens; i++ {
			if _, _, _, err := rm.ExecuteTrampoline("h", "urn:hdm:test:rpn2", "run-tick", 0, 0); err != nil {
				t.Fatalf("rpn: %v", err)
			}
		}
		return r(stkBase)
	}

	cases := []struct {
		text string
		want int32
	}{
		{"34+", 7},                // 3 4 +
		{"34+2*", 14},             // (3+4)*2
		{"82/", 4},                // 8 / 2
		{"5 1 2 + 4 * + 3 -", 14}, // 5 + (1+2)*4 - 3, with spaces
		{"9 3 /", 3},              // 9/3
		{"7 2 - 5 *", 25},         // (7-2)*5
	}
	for _, c := range cases {
		if got := parse(c.text); got != c.want {
			t.Errorf("parse(%q) = %d, want %d", c.text, got, c.want)
		}
	}
}

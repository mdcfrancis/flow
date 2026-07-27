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

// A PARSER CELL authored entirely in Flux, using the buffer capability: a tokenizer
// step that each tick reads src[cursor], classifies the byte (digit → 1, else 0),
// STORES the token into dst[cursor], and advances the cursor. Driven for N ticks (as
// the app frame loop would), it tokenizes the whole input buffer — a stream-processing
// cell that needs no hand-WAT. This is what the buffer capability unlocks: parser
// cells are Flux (docs/flux-surface-ir.md).
func TestFluxTokenizerParserCell(t *testing.T) {
	ctx := context.Background()
	le, _ := storage.NewLedgerEngine(filepath.Join(t.TempDir(), "hdm.db"))
	defer le.Close()
	rm, err := NewRuntimeManager(ctx, le, inference.NewLocalModelClient("http://127.0.0.1:0", "test"))
	if err != nil {
		t.Fatalf("runtime: %v", err)
	}
	defer rm.Close(ctx)
	cs := compiler.NewCompilerService()

	const srcBase, dstBase, cursorOff = 0xB2000, 0xB3000, 0xB0000
	const n = 8
	layout := flux.Layout{
		"src":    {Type: flux.TBuffer, Offset: srcBase, Len: n},
		"dst":    {Type: flux.TBuffer, Offset: dstBase, Len: n},
		"cursor": {Type: flux.TInt, Offset: cursorOff},
	}
	// digit? = 48 <= src[cursor] <= 57 ; write it to dst[cursor]; advance the cursor.
	src := `(cell tokenize (reads src cursor) (writes dst cursor)
	  (let ([b (at src cursor)])
	    (write
	      (store dst cursor (if (and (>= b 48) (<= b 57)) 1 0))
	      (cursor (+ cursor 1)))))`
	wat, err := flux.Compile("m", src, layout)
	if err != nil {
		t.Fatalf("flux compile: %v", err)
	}
	loadWAT(t, rm, cs, "urn:hdm:test:tokenize", wat)

	w := func(off, v uint32) {
		var b [4]byte
		binary.LittleEndian.PutUint32(b[:], v)
		rm.sharedMem.Write(off, b[:])
	}
	r := func(off uint32) uint32 { v, _ := rm.PeekU32(off); return v }

	// input bytes: "a1b23c45" → digits at positions 1,3,4,6,7
	input := []uint32{'a', '1', 'b', '2', '3', 'c', '4', '5'}
	for i, v := range input {
		w(srcBase+uint32(i*4), v)
	}
	w(cursorOff, 0)

	// Drive the cell n ticks, like the frame loop.
	for tick := 0; tick < n; tick++ {
		if _, _, _, err := rm.ExecuteTrampoline("h", "urn:hdm:test:tokenize", "run-tick", 0, 0); err != nil {
			t.Fatalf("tick %d: %v", tick, err)
		}
	}

	want := []uint32{0, 1, 0, 1, 1, 0, 1, 1} // 1 where the input byte is a digit
	for i := range want {
		if got := r(dstBase + uint32(i*4)); got != want[i] {
			t.Fatalf("token[%d] = %d, want %d (input %q)", i, got, want[i], rune(input[i]))
		}
	}
	if c := r(cursorOff); c != n {
		t.Fatalf("cursor advanced to %d, want %d", c, n)
	}
}

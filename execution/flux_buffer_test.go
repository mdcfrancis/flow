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

// A Flux cell that INDEXES a buffer — (at tokens cursor) — assembles and runs in the
// real sandbox: it reads tokens[cursor] and the access is bounds-CLAMPED, so an
// out-of-range cursor reads the nearest in-range element rather than escaping the
// buffer. This proves the buffer capability that makes parser/stream cells authorable
// in Flux (docs/flux-surface-ir.md).
func TestFluxBufferAtRunsInSandbox(t *testing.T) {
	ctx := context.Background()
	le, _ := storage.NewLedgerEngine(filepath.Join(t.TempDir(), "hdm.db"))
	defer le.Close()
	rm, err := NewRuntimeManager(ctx, le, inference.NewLocalModelClient("http://127.0.0.1:0", "test"))
	if err != nil {
		t.Fatalf("runtime: %v", err)
	}
	defer rm.Close(ctx)
	cs := compiler.NewCompilerService()

	const tokensBase, cursorOff, outOff = 0xB1000, 0xB0000, 0xB0004
	layout := flux.Layout{
		"tokens": {Type: flux.TBuffer, Offset: tokensBase, Len: 8},
		"cursor": {Type: flux.TInt, Offset: cursorOff, ReadOnly: true},
		"out":    {Type: flux.TInt, Offset: outOff},
	}
	wat, err := flux.Compile("m", `(cell peek (reads tokens cursor) (writes out) (write (out (at tokens cursor))))`, layout)
	if err != nil {
		t.Fatalf("flux compile: %v", err)
	}
	loadWAT(t, rm, cs, "urn:hdm:test:peek", wat)

	w := func(off, v uint32) {
		var b [4]byte
		binary.LittleEndian.PutUint32(b[:], v)
		rm.sharedMem.Write(off, b[:])
	}
	r := func(off uint32) uint32 { v, _ := rm.PeekU32(off); return v }

	toks := []uint32{10, 20, 30, 40, 50, 60, 70, 80}
	for i, v := range toks {
		w(tokensBase+uint32(i*4), v)
	}
	peek := func(cursor int32) uint32 {
		w(cursorOff, uint32(cursor))
		if _, _, _, err := rm.ExecuteTrampoline("h", "urn:hdm:test:peek", "run-tick", 0, 0); err != nil {
			t.Fatalf("run (cursor=%d): %v", cursor, err)
		}
		return r(outOff)
	}

	// In-range: reads tokens[cursor].
	for i, want := range toks {
		if got := peek(int32(i)); got != want {
			t.Fatalf("tokens[%d] = %d, want %d", i, got, want)
		}
	}
	// Out-of-range: clamped to the nearest in-range element (never escapes the window).
	if got := peek(99); got != 80 {
		t.Fatalf("cursor=99 clamps to tokens[7]=80, got %d", got)
	}
	if got := peek(-5); got != 10 {
		t.Fatalf("cursor=-5 clamps to tokens[0]=10, got %d", got)
	}
}

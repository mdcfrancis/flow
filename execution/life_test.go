package execution

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/mdcfrancis/flow/compiler"
	"github.com/mdcfrancis/flow/storage"
)

// TestLifeCellEvolves compiles the Game-of-Life UI cell, renders two frames, and
// verifies it emits a non-trivial vector stream that changes between generations
// (state persists in shared memory across render-frame ticks).
func TestLifeCellEvolves(t *testing.T) {
	ctx := context.Background()
	wat, err := os.ReadFile(filepath.Join("..", "cells", "life.wat"))
	if err != nil {
		t.Fatalf("read life.wat: %v", err)
	}
	art, err := compiler.NewCompilerService().CompileGenotype(string(wat))
	if err != nil || !art.SyntaxPassed {
		t.Fatalf("compile life: %v (%s)", err, art.ErrorContext)
	}

	le, err := storage.NewLedgerEngine(filepath.Join(t.TempDir(), "hdm.db"))
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	defer le.Close()
	rm, err := NewRuntimeManager(ctx, le, nil)
	if err != nil {
		t.Fatalf("runtime: %v", err)
	}
	defer rm.Close(ctx)
	if err := rm.LoadCell("urn:hdm:ui:life", art.Bytecode); err != nil {
		t.Fatalf("load life: %v", err)
	}

	f1, err := rm.RenderFrame("urn:hdm:ui:life")
	if err != nil {
		t.Fatalf("frame 1: %v", err)
	}
	// Background record + at least some live cells, all whole 24-byte records.
	if len(f1) < 24 || len(f1)%24 != 0 {
		t.Fatalf("frame 1 = %d bytes (want a multiple of 24, >24)", len(f1))
	}
	if len(f1) <= 24 {
		t.Fatal("seeded frame has no live cells")
	}

	f2, err := rm.RenderFrame("urn:hdm:ui:life")
	if err != nil {
		t.Fatalf("frame 2: %v", err)
	}
	// One generation later the board must have changed.
	if bytes.Equal(f1, f2) {
		t.Fatal("life board did not evolve between generations")
	}
}

// TestLifeCellReactsToClick verifies the Peripheral Input Gateway is wired end
// to end: a click latched into the HMI register is consumed by the cell's
// $inject on the next render-frame, forcing a 2x2 live block at the clicked
// grid cell (canvas coords / 8). The block is applied after $step, so the four
// grid bytes are deterministically alive regardless of the seeded field.
func TestLifeCellReactsToClick(t *testing.T) {
	ctx := context.Background()
	wat, err := os.ReadFile(filepath.Join("..", "cells", "life.wat"))
	if err != nil {
		t.Fatalf("read life.wat: %v", err)
	}
	art, err := compiler.NewCompilerService().CompileGenotype(string(wat))
	if err != nil || !art.SyntaxPassed {
		t.Fatalf("compile life: %v (%s)", err, art.ErrorContext)
	}
	le, err := storage.NewLedgerEngine(filepath.Join(t.TempDir(), "hdm.db"))
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	defer le.Close()
	rm, err := NewRuntimeManager(ctx, le, nil)
	if err != nil {
		t.Fatalf("runtime: %v", err)
	}
	defer rm.Close(ctx)
	if err := rm.LoadCell("urn:hdm:ui:life", art.Bytecode); err != nil {
		t.Fatalf("load life: %v", err)
	}

	// Frame 1 seeds; no input yet so $inject is a no-op (eventSeq 0).
	if _, err := rm.RenderFrame("urn:hdm:ui:life"); err != nil {
		t.Fatalf("frame 1: %v", err)
	}
	// Click at canvas (80,80) -> grid cell (10,10).
	if err := rm.WriteInputEvent(InputEvent{Type: EvClick, X: 80, Y: 80, Buttons: 1}); err != nil {
		t.Fatalf("click: %v", err)
	}
	if _, err := rm.RenderFrame("urn:hdm:ui:life"); err != nil {
		t.Fatalf("frame 2: %v", err)
	}

	// The 2x2 block at (10,10) must now be alive in the grid (base 0x90000,
	// row stride 40).
	const gridBase = 0x90000
	alive := func(gx, gy int) bool {
		b, ok := rm.sharedMem.Read(uint32(gridBase+gy*40+gx), 1)
		return ok && b[0] == 1
	}
	for _, c := range [][2]int{{10, 10}, {11, 10}, {10, 11}, {11, 11}} {
		if !alive(c[0], c[1]) {
			t.Fatalf("clicked cell (%d,%d) not alive", c[0], c[1])
		}
	}
	// The event must be marked consumed (last-seen seq slot at 0x9F004 == 1).
	if got := rm.readU32(0x9F004); got != 1 {
		t.Fatalf("consumed-seq slot = %d, want 1", got)
	}
}

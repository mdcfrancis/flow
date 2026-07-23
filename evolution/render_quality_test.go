package evolution

import (
	"path/filepath"
	"testing"

	"github.com/mdcfrancis/flow/storage"
)

func TestMatchDrawBrightSpread(t *testing.T) {
	frame := func(rgbas ...uint32) [][]DrawRecord {
		var recs []DrawRecord
		for i, c := range rgbas {
			recs = append(recs, DrawRecord{Op: 1, A: int32(i), B: 0, RGBA: c})
		}
		return [][]DrawRecord{recs}
	}
	d := DrawExpect{MinBrightSpread: 110}
	// A blank/near-uniform dark render (distinct colors but no contrast) must FAIL.
	if matchDraw(d, frame(0x000000ff, 0x101010ff, 0x080808ff, 0x121212ff)) {
		t.Error("all-dark colors should fail the brightness-spread floor")
	}
	// A render whose colors span dark→bright passes.
	if !matchDraw(d, frame(0x000000ff, 0x404040ff, 0x808080ff, 0xffffffff)) {
		t.Error("a dark→bright gradient should pass")
	}
	// MinBrightSpread composes with MinColors: a 2-color dark/bright ping-pong has
	// contrast but too few colors for a gradient.
	d2 := DrawExpect{MinColors: 8, MinBrightSpread: 110}
	if matchDraw(d2, frame(0x000000ff, 0xffffffff, 0x000000ff, 0xffffffff)) {
		t.Error("2 colors should fail MinColors even with contrast")
	}
}

func TestCriticNoteRoundTrip(t *testing.T) {
	le, err := storage.NewLedgerEngine(filepath.Join(t.TempDir(), "hdm.db"))
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	defer le.Close()
	urn := "urn:hdm:apps:x:renderer"
	if LoadCriticNote(le, urn) != "" {
		t.Error("expected no note initially")
	}
	if err := SaveCriticNote(le, urn, "almost entirely one color; no visible fractal"); err != nil {
		t.Fatalf("save: %v", err)
	}
	if got := LoadCriticNote(le, urn); got != "almost entirely one color; no visible fractal" {
		t.Errorf("note=%q", got)
	}
	if err := SaveCriticNote(le, urn, ""); err != nil { // clear on satisfied
		t.Fatalf("clear: %v", err)
	}
	if LoadCriticNote(le, urn) != "" {
		t.Error("expected note cleared")
	}
}

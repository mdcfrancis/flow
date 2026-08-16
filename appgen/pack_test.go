package appgen

import (
	"testing"

	"github.com/mdcfrancis/flow/evolution"
)

// packOffsets must give an i32[N] array its full N*4 bytes so the next field lands
// after it — the Mandelbrot bug was a grid array that got no space (or was a scalar).
func TestPackOffsetsReservesArraySpace(t *testing.T) {
	in := []evolution.ContractField{
		{Name: "grid", Type: "i32[3072]", Offset: 0xB0000}, // 64x48 escape grid
		{Name: "max_iter", Type: "i32", Offset: 0xB0004},   // model put it overlapping
		{Name: "zoom", Type: "i32", Offset: 0xB0008},
	}
	out := packOffsets(in, evolution.LegacyArenaBase)
	if len(out) != 3 {
		t.Fatalf("expected 3 fields, got %d", len(out))
	}
	if out[0].Offset != 0xB0000 {
		t.Errorf("grid should start at region base, got 0x%X", out[0].Offset)
	}
	// grid spans 3072*4 = 0x3000 bytes, so max_iter must start at 0xB0000+0x3000
	wantMaxIter := 0xB0000 + 3072*4
	if out[1].Offset != wantMaxIter {
		t.Errorf("max_iter should start after the array at 0x%X, got 0x%X (overlap!)", wantMaxIter, out[1].Offset)
	}
	if out[2].Offset != wantMaxIter+4 {
		t.Errorf("zoom should follow max_iter at 0x%X, got 0x%X", wantMaxIter+4, out[2].Offset)
	}
}

func TestPackOffsetsDropsOversizedField(t *testing.T) {
	in := []evolution.ContractField{
		{Name: "ok", Type: "i32"},
		{Name: "huge", Type: "i32[100000]"}, // 400KB > 64KB region -> dropped
		{Name: "after", Type: "i32"},
	}
	out := packOffsets(in, evolution.LegacyArenaBase)
	names := map[string]bool{}
	for _, f := range out {
		names[f.Name] = true
	}
	if names["huge"] {
		t.Error("a field spilling past the contract region must be dropped")
	}
	if !names["ok"] || !names["after"] {
		t.Error("in-region fields must survive")
	}
}

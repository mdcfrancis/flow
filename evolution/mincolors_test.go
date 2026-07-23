package evolution

import "testing"

func TestMatchDrawMinColors(t *testing.T) {
	flat := [][]DrawRecord{{{RGBA: 0x111111ff}, {RGBA: 0x111111ff}, {RGBA: 0x111111ff}}}
	varied := [][]DrawRecord{{{RGBA: 0x111111ff}, {RGBA: 0x22ff33ff}, {RGBA: 0xff0000ff}, {RGBA: 0x0000ffff}}}
	d := DrawExpect{MinColors: 4}
	if matchDraw(d, flat) {
		t.Error("a 1-color fill must fail MinColors=4")
	}
	if !matchDraw(d, varied) {
		t.Error("a 4-distinct-color frame must pass MinColors=4")
	}
	// MinRecords still works alongside
	d2 := DrawExpect{MinRecords: 3, MinColors: 2}
	if !matchDraw(d2, varied) {
		t.Error("varied frame (4 recs, 4 colors) must pass MinRecords=3 MinColors=2")
	}
}

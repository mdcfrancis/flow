package flux

import "testing"

// Clause-less Flux: reads/writes are derived from the body, and it still lowers.
func TestDerivedReadsWrites(t *testing.T) {
	layout := Layout{
		"ball_x": {Type: TInt, Offset: 0xB0000}, "ball_y": {Type: TInt, Offset: 0xB0004},
		"ball_vx": {Type: TInt, Offset: 0xB0008},
	}
	src := `(cell c (write (ball_x (+ ball_x ball_vx))))`
	f, err := Parse("c", src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	cell, err := Check(f, layout)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if got := cell.Reads; len(got) != 2 || got[0] != "ball_x" || got[1] != "ball_vx" {
		t.Fatalf("derived reads = %v, want [ball_x ball_vx]", got)
	}
	if got := cell.Writes; len(got) != 1 || got[0] != "ball_x" {
		t.Fatalf("derived writes = %v, want [ball_x]", got)
	}
	if _, err := Compile("c", src, layout); err != nil {
		t.Fatalf("clause-less cell did not lower: %v", err)
	}
}

// A view with no reads clause derives reads from the draw.
func TestDerivedReadsView(t *testing.T) {
	layout := Layout{"ball_x": {Type: TInt, Offset: 0xB0000}, "ball_y": {Type: TInt, Offset: 0xB0004}}
	src := `(cell c (draw (circle ball_x ball_y 8 #xFFCC33FF)))`
	f, _ := Parse("c", src)
	cell, err := Check(f, layout)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if len(cell.Reads) != 2 {
		t.Fatalf("derived view reads = %v, want ball_x, ball_y", cell.Reads)
	}
}

package execution

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mdcfrancis/flow/flux"
	"github.com/mdcfrancis/flow/inference"
	"github.com/mdcfrancis/flow/storage"
)

// The self-hosted AST parser, driven as a LIVE cell, parses the canonical physics cell
// (lets + clamp + wall-reflect, four writes) into AST records; Go reconstruction turns
// those records back into Flux source that compiles BYTE-IDENTICAL to the same cell
// compiled directly by the Go parser. The authoring parse path, self-hosted end to end.
func TestRunSelfHostASTParse(t *testing.T) {
	ctx := context.Background()
	le, _ := storage.NewLedgerEngine(filepath.Join(t.TempDir(), "hdm.db"))
	defer le.Close()
	rm, err := NewRuntimeManager(ctx, le, inference.NewLocalModelClient("http://127.0.0.1:0", "test"))
	if err != nil {
		t.Fatalf("runtime: %v", err)
	}
	defer rm.Close(ctx)

	f := func(i int) int32 { return int32(-100 - i) }
	lref := func(i int) int32 { return int32(-300 - i) }
	wr := func(i int) int32 { return int32(-200 - i) }
	lb := func(i int) int32 { return int32(-400 - i) }
	const add, sub, lt, ge, or, neg, iff, clamp = -1, -2, -6, -9, -13, -16, -19, -20
	toks := []int32{
		f(0), f(2), add, lb(0), // t0 = ball_x + vel_x
		f(1), f(3), add, lb(1), // t1 = ball_y + vel_y
		lref(0), 0, f(4), 1, sub, clamp, wr(0), // ball_x = clamp(t0, 0, screen_w-1)
		lref(1), 0, f(5), 1, sub, clamp, wr(1), // ball_y = clamp(t1, 0, screen_h-1)
		lref(0), 0, lt, lref(0), f(4), ge, or, f(2), neg, f(2), iff, wr(2), // vel_x reflect
		lref(1), 0, lt, lref(1), f(5), ge, or, f(3), neg, f(3), iff, wr(3), // vel_y reflect
	}

	res, err := rm.RunSelfHostASTParse(toks)
	if err != nil {
		t.Fatalf("RunSelfHostASTParse: %v", err)
	}
	fields := []string{"ball_x", "ball_y", "vel_x", "vel_y", "screen_w", "screen_h"}
	reads := fields
	writes := []string{"ball_x", "ball_y", "vel_x", "vel_y"}
	got, err := ReconstructCell(res, "c", fields, reads, writes)
	if err != nil {
		t.Fatalf("ReconstructCell: %v", err)
	}

	want := `(cell c (reads ball_x ball_y vel_x vel_y screen_w screen_h) (writes ball_x ball_y vel_x vel_y)
	  (let ([t0 (+ ball_x vel_x)] [t1 (+ ball_y vel_y)])
	    (write (ball_x (clamp t0 0 (- screen_w 1)))
	           (ball_y (clamp t1 0 (- screen_h 1)))
	           (vel_x (if (or (< t0 0) (>= t0 screen_w)) (neg vel_x) vel_x))
	           (vel_y (if (or (< t1 0) (>= t1 screen_h)) (neg vel_y) vel_y)))))`
	cellLayout := flux.Layout{
		"ball_x": {Type: flux.TInt, Offset: 0xC0000}, "ball_y": {Type: flux.TInt, Offset: 0xC0004},
		"vel_x": {Type: flux.TInt, Offset: 0xC0008}, "vel_y": {Type: flux.TInt, Offset: 0xC000C},
		"screen_w": {Type: flux.TInt, Offset: 0xC0010, ReadOnly: true}, "screen_h": {Type: flux.TInt, Offset: 0xC0014, ReadOnly: true},
	}
	gotWAT, err := flux.Compile("m", got, cellLayout)
	if err != nil {
		t.Fatalf("compile reconstructed cell:\n%s\nerr: %v", got, err)
	}
	wantWAT, err := flux.Compile("m", want, cellLayout)
	if err != nil {
		t.Fatalf("compile canonical cell: %v", err)
	}
	if gotWAT != wantWAT {
		t.Errorf("self-hosted parse != Go compiler.\nreconstructed: %s", got)
	}
}

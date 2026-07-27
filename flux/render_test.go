package flux

import "testing"

// The programs below span the surface: view (draw), compute with a let, and nested
// expressions (if/clamp/or). Layout covers every field they touch.
var rtLayout = Layout{
	"ball_x": {Type: TInt, Offset: 0xB0000}, "ball_y": {Type: TInt, Offset: 0xB0004},
	"vel_x": {Type: TInt, Offset: 0xB0008}, "vel_y": {Type: TInt, Offset: 0xB000C},
	"screen_w": {Type: TInt, Offset: 0xB0010, ReadOnly: true}, "screen_h": {Type: TInt, Offset: 0xB0014, ReadOnly: true},
}

var rtProgks = []string{
	`(cell renderer (reads ball_x ball_y) (draw (circle ball_x ball_y 8 #xFFCC33FF)))`,
	`(cell physics (reads ball_x ball_y vel_x vel_y screen_w screen_h) (writes ball_x ball_y vel_x vel_y)
	  (let ([nx (+ ball_x vel_x)] [ny (+ ball_y vel_y)])
	    (write (ball_x (clamp nx 0 (- screen_w 1))) (ball_y (clamp ny 0 (- screen_h 1)))
	           (vel_x (if (or (< nx 0) (>= nx screen_w)) (neg vel_x) vel_x))
	           (vel_y (if (or (< ny 0) (>= ny screen_h)) (neg vel_y) vel_y)))))`,
	`(cell mover (reads ball_x vel_x) (writes ball_x) (write (ball_x (+ ball_x vel_x))))`,
}

// THE INVARIANT (docs/flux-surface-ir.md): the surface is a serialization of the IR,
// so text → IR → text → IR round-trips, and — the behavior guarantee — a cell and
// its re-read render lower to IDENTICAL WAT. Behavior is a property of the IR, not
// the surface text.
func TestSurfaceRoundTripPreservesBehavior(t *testing.T) {
	for _, src := range rtProgks {
		cell1, err := DefaultSurface.Read("m", src, rtLayout)
		if err != nil {
			t.Fatalf("read 1: %v\n%s", err, src)
		}
		rendered := DefaultSurface.Render(cell1)
		cell2, err := DefaultSurface.Read("m", rendered, rtLayout)
		if err != nil {
			t.Fatalf("re-read of rendered IR failed: %v\nrendered:\n%s", err, rendered)
		}
		if !SameBehavior(cell1, cell2, rtLayout) {
			w1, _ := Lower(cell1, rtLayout)
			w2, _ := Lower(cell2, rtLayout)
			t.Fatalf("round-trip changed behavior for %q\n--- WAT1 ---\n%s\n--- WAT2 ---\n%s", cell1.Name, w1, w2)
		}
	}
}

// Render is idempotent: rendering, re-reading, and rendering again yields identical
// surface text — the serialization has a fixed point (a canonicality property).
func TestRenderIsIdempotent(t *testing.T) {
	for _, src := range rtProgks {
		cell, err := DefaultSurface.Read("m", src, rtLayout)
		if err != nil {
			t.Fatal(err)
		}
		once := DefaultSurface.Render(cell)
		cell2, err := DefaultSurface.Read("m", once, rtLayout)
		if err != nil {
			t.Fatalf("re-read: %v\n%s", err, once)
		}
		if twice := DefaultSurface.Render(cell2); once != twice {
			t.Fatalf("render not idempotent:\n--- once ---\n%s\n--- twice ---\n%s", once, twice)
		}
	}
}

// BehaviorHash is stable and discriminating: the same cell hashes the same, and two
// cells with different logic hash differently.
func TestBehaviorHashDiscriminates(t *testing.T) {
	a, _ := DefaultSurface.Read("m", rtProgks[2], rtLayout) // (+ ball_x vel_x)
	b, _ := DefaultSurface.Read("m", `(cell mover (reads ball_x vel_x) (writes ball_x) (write (ball_x (- ball_x vel_x))))`, rtLayout)
	ha, err := BehaviorHash(a, rtLayout)
	if err != nil {
		t.Fatal(err)
	}
	hb, _ := BehaviorHash(b, rtLayout)
	if ha == hb {
		t.Fatal("cells with different logic must not share a BehaviorHash")
	}
	// determinism
	if ha2, _ := BehaviorHash(a, rtLayout); ha != ha2 {
		t.Fatal("BehaviorHash must be deterministic")
	}
}

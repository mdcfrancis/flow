package flux

import "testing"

var forthLayout = Layout{
	"ball_x": {Type: TInt, Offset: 0xB0000}, "ball_y": {Type: TInt, Offset: 0xB0004},
	"vel_x": {Type: TInt, Offset: 0xB0008}, "vel_y": {Type: TInt, Offset: 0xB000C},
	"screen_w": {Type: TInt, Offset: 0xB0010, ReadOnly: true}, "screen_h": {Type: TInt, Offset: 0xB0014, ReadOnly: true},
}

// A Forth cell reads to the SAME IR (hence the same WAT) as the equivalent S-expr
// cell — the cross-surface behavior invariant that makes them A/B-able.
func TestForthReadsToSameIRAsSExpr(t *testing.T) {
	cases := []struct{ forth, sexpr string }{
		{
			forth: `ball_x vel_x + -> ball_x`,
			sexpr: `(cell c (write (ball_x (+ ball_x vel_x))))`,
		},
		{
			forth: `ball_x ball_y 8 #xFFCC33FF circle`,
			sexpr: `(cell c (draw (circle ball_x ball_y 8 #xFFCC33FF)))`,
		},
		{
			// prologue locals + clamp + if(?) — the shallow-stack shape
			forth: `ball_x vel_x + =: nx
			        nx 0 screen_w 1 - clamp -> ball_x
			        nx 0 < nx screen_w >= or  vel_x neg  vel_x  ? -> vel_x`,
			sexpr: `(cell c
			          (let ([nx (+ ball_x vel_x)])
			            (write (ball_x (clamp nx 0 (- screen_w 1)))
			                   (vel_x (if (or (< nx 0) (>= nx screen_w)) (neg vel_x) vel_x)))))`,
		},
	}
	for _, c := range cases {
		fc, err := Forth{}.Read("m", c.forth, forthLayout)
		if err != nil {
			t.Fatalf("forth read: %v\n%s", err, c.forth)
		}
		sc, err := SExpr{}.Read("m", c.sexpr, forthLayout)
		if err != nil {
			t.Fatalf("sexpr read: %v", err)
		}
		if !SameBehavior(fc, sc, forthLayout) {
			wf, _ := Lower(fc, forthLayout)
			ws, _ := Lower(sc, forthLayout)
			t.Fatalf("forth and s-expr disagree:\n%s\n--- forth WAT ---\n%s\n--- sexpr WAT ---\n%s", c.forth, wf, ws)
		}
	}
}

// Render → Read round-trips through the IR to identical behavior, for both surfaces.
func TestForthRoundTripPreservesBehavior(t *testing.T) {
	seeds := []string{
		`(cell c (write (ball_x (+ ball_x vel_x))))`,
		`(cell c (draw (circle ball_x ball_y 8 #xFFCC33FF)))`,
		`(cell c (let ([nx (+ ball_x vel_x)] [ny (+ ball_y vel_y)])
		           (write (ball_x (clamp nx 0 (- screen_w 1)))
		                  (ball_y (clamp ny 0 (- screen_h 1)))
		                  (vel_x (if (or (< nx 0) (>= nx screen_w)) (neg vel_x) vel_x))
		                  (vel_y (if (or (< ny 0) (>= ny screen_h)) (neg vel_y) vel_y)))))`,
	}
	for _, s := range seeds {
		cell, err := SExpr{}.Read("m", s, forthLayout)
		if err != nil {
			t.Fatal(err)
		}
		forth := Forth{}.Render(cell)
		back, err := Forth{}.Read("m", forth, forthLayout)
		if err != nil {
			t.Fatalf("re-read of rendered forth failed: %v\nrendered: %s", err, forth)
		}
		if !SameBehavior(cell, back, forthLayout) {
			t.Fatalf("forth round-trip changed behavior\nrendered: %s", forth)
		}
	}
}

// Malformed Forth is rejected (so the scoreboard counts it invalid, never crashes).
func TestForthRejectsMalformed(t *testing.T) {
	bad := []string{
		`ball_x vel_x +`,            // dangling value (never written)
		`+ -> ball_x`,               // underflow
		`ball_x nonsense -> ball_x`, // unknown word
		`ball_x -> `,                // -> without field
	}
	for _, s := range bad {
		if _, err := (Forth{}).Read("m", s, forthLayout); err == nil {
			t.Fatalf("expected %q to be rejected", s)
		}
	}
}

// The prologue keeps stacks shallow: rendering physics, the max operand depth between
// bindings/writes stays small (the local-introspection property).
func TestForthRenderIsTokenLean(t *testing.T) {
	cell, _ := SExpr{}.Read("m",
		`(cell c (write (ball_x (+ ball_x vel_x))))`, forthLayout)
	forth := Forth{}.Render(cell)
	sexpr := Render(cell)
	if len(forth) >= len(sexpr) {
		t.Fatalf("forth should be leaner than s-expr for a simple cell:\n forth: %q (%d)\n sexpr: %q (%d)",
			forth, len(forth), sexpr, len(sexpr))
	}
}

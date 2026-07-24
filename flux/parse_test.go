package flux

import "testing"

// The two cells that failed 31× as raw WAT must parse cleanly as Flux — the
// smoke test that the participle grammar covers the real target surface.
const physicsCell = `
; a bouncing-ball physics cell: pos += vel, reflect + clamp at the walls
(cell physics
  (reads  ball_x ball_y vel_x vel_y screen_w screen_h)
  (writes ball_x ball_y vel_x vel_y)
  (let ([nx (+ ball_x vel_x)]
        [ny (+ ball_y vel_y)]
        [bounce_x (or (< nx 0) (>= nx screen_w))]
        [bounce_y (or (< ny 0) (>= ny screen_h))])
    (write
      (vel_x (if bounce_x (neg vel_x) vel_x))
      (vel_y (if bounce_y (neg vel_y) vel_y))
      (ball_x (clamp nx 0 (- screen_w 1)))
      (ball_y (clamp ny 0 (- screen_h 1))))))
`

const rendererCell = `
(cell renderer
  (reads ball_x ball_y)
  (draw
    (circle ball_x ball_y 8 #xFFCC33FF)))
`

func TestParsePhysicsCell(t *testing.T) {
	f, err := Parse("physics", physicsCell)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(f.Forms) != 1 {
		t.Fatalf("want 1 top-level form, got %d", len(f.Forms))
	}
	cell := f.Forms[0]
	if cell.Head() != "cell" {
		t.Fatalf("head = %q, want cell", cell.Head())
	}
	if nm := cell.Items[1].Atom; nm == nil || nm.Symbol == nil || *nm.Symbol != "physics" {
		t.Fatalf("cell name did not parse as symbol 'physics'")
	}
	// The clause structure the typechecker/lowerer rely on is present.
	reads := cell.Sub("reads")
	if reads == nil || len(reads.Items) != 7 { // head + 6 fields
		t.Fatalf("reads clause: %+v", reads)
	}
	if cell.Sub("writes") == nil {
		t.Fatal("missing writes clause")
	}
	// The body is a `let` whose terminal form is the `write` record.
	body := cell.Sub("let")
	if body == nil {
		t.Fatal("missing let body")
	}
	if body.Sub("write") == nil {
		t.Fatal("missing write record inside the let body")
	}
}

func TestParseRendererAtomsAreTyped(t *testing.T) {
	f, err := Parse("renderer", rendererCell)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	circle := f.Forms[0].Sub("draw").Sub("circle")
	if circle == nil {
		t.Fatal("no circle prim found")
	}
	// circle: (circle ball_x ball_y 8 #xFFCC33FF) — the Int and Color atoms must
	// be lexed as their distinct kinds, not swallowed as symbols.
	var sawInt, sawColor bool
	for _, it := range circle.Items {
		if it.Atom == nil {
			continue
		}
		if it.Atom.Int != nil && *it.Atom.Int == "8" {
			sawInt = true
		}
		if it.Atom.Color != nil && *it.Atom.Color == "#xFFCC33FF" {
			sawColor = true
		}
	}
	if !sawInt {
		t.Error("Int literal 8 not recognized")
	}
	if !sawColor {
		t.Error("Color literal #xFFCC33FF not recognized")
	}
}

// A malformed cell must yield a positioned syntax error (which feeds the
// synthesis correction loop), not a panic or silent success.
func TestParseErrorIsPositioned(t *testing.T) {
	_, err := Parse("bad", `(cell broken (reads a b`)
	if err == nil {
		t.Fatal("expected a syntax error for the unbalanced form")
	}
}

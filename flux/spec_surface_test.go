package flux

import (
	"strings"
	"testing"
)

// A surface defined purely as DATA (a SurfaceSpec) reads and renders through the
// generic engine to the SAME IR as the canonical S-expression — so it is a real,
// behavior-safe surface with no Go code of its own. This is "the parser as data".
func TestSpecSurfaceRoundTripsToSameIR(t *testing.T) {
	layout := rtLayout // from render_test.go
	// A full keyword skin defined entirely as DATA: short structural tokens.
	spec := SurfaceSpec{
		Name:    "terse-skin",
		Keyword: map[string]string{"cell": "c", "reads": "r", "writes": "w", "write": "!", "let": "lt"},
	}
	surf := SpecSurface{Spec: spec}

	canonical := `(cell physics (reads ball_x ball_y vel_x vel_y screen_w screen_h) (writes ball_x ball_y vel_x vel_y)
	  (let ([nx (+ ball_x vel_x)] [ny (+ ball_y vel_y)])
	    (write (ball_x (clamp nx 0 (- screen_w 1))) (ball_y (clamp ny 0 (- screen_h 1)))
	           (vel_x (if (or (< nx 0) (>= nx screen_w)) (neg vel_x) vel_x))
	           (vel_y (if (or (< ny 0) (>= ny screen_h)) (neg vel_y) vel_y)))))`

	cell, err := SExpr{}.Read("m", canonical, layout)
	if err != nil {
		t.Fatal(err)
	}
	skinned := surf.Render(cell)
	// The skin uses the data-defined tokens.
	if !strings.HasPrefix(skinned, "(c physics (r ball_x") || !strings.Contains(skinned, "(w ball_x") || !strings.Contains(skinned, "(lt (") || !strings.Contains(skinned, "(! (") {
		t.Fatalf("spec render did not apply the skin:\n%s", skinned)
	}
	// It reads back to the same behavior — the data-defined surface is a real surface.
	back, err := surf.Read("m", skinned, layout)
	if err != nil {
		t.Fatalf("re-read of the skinned surface failed: %v\n%s", err, skinned)
	}
	if !SameBehavior(cell, back, layout) {
		t.Fatalf("spec surface changed behavior:\n%s", skinned)
	}
}

// A view cell works too, and operator renaming is honored.
func TestSpecSurfaceViewAndOpRename(t *testing.T) {
	layout := rtLayout
	spec := SurfaceSpec{Name: "renamed", Keyword: map[string]string{"cell": "cellx", "draw": "paint", "circle": "dot"}}
	surf := SpecSurface{Spec: spec}
	cell, err := SExpr{}.Read("m", `(cell v (reads ball_x ball_y) (draw (circle ball_x ball_y 8 #xFFCC33FF)))`, layout)
	if err != nil {
		t.Fatal(err)
	}
	skinned := surf.Render(cell)
	if !strings.Contains(skinned, "(paint (dot ") {
		t.Fatalf("op/keyword rename not applied:\n%s", skinned)
	}
	back, err := surf.Read("m", skinned, layout)
	if err != nil {
		t.Fatalf("re-read failed: %v\n%s", err, skinned)
	}
	if !SameBehavior(cell, back, layout) {
		t.Fatal("view spec surface changed behavior")
	}
}

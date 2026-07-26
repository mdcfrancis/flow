package flux

import (
	"strings"
	"testing"
)

var canonLayout = Layout{
	"ball_x": {Type: TInt, Offset: 0xB0000}, "ball_y": {Type: TInt, Offset: 0xB0004},
	"vel_x": {Type: TInt, Offset: 0xB0008}, "vel_y": {Type: TInt, Offset: 0xB000C},
	"screen_w": {Type: TInt, Offset: 0xB0010, ReadOnly: true}, "screen_h": {Type: TInt, Offset: 0xB0014, ReadOnly: true},
}

// The canonical grammar emits fixed-order optional write slots and NO free-order
// write-pair list — that is the mechanism that collapses the N! orderings.
func TestGBNFCanonicalHasFixedWriteSlots(t *testing.T) {
	g := GBNFCanonical(canonLayout, KindCompute)
	if !strings.Contains(g, "w0 ::=") || !strings.Contains(g, "w3 ::=") {
		t.Fatalf("expected one fixed slot per writable field:\n%s", g)
	}
	if strings.Contains(g, "pair (") {
		t.Fatalf("canonical grammar must not have the free-order write-pair list:\n%s", g)
	}
	// The writes clause is fixed to all writable fields in sorted order.
	if !strings.Contains(g, "(writes ball_x ball_y vel_x vel_y)") {
		t.Fatalf("expected a fixed (writes …) clause in canonical order:\n%s", g)
	}
	// The baseline still uses the free-order pair list (nothing regressed).
	if b := GBNF(canonLayout, KindCompute); !strings.Contains(b, "pair (") {
		t.Fatalf("baseline should keep the free-order write pairs:\n%s", b)
	}
}

// A program written in canonical order is valid Flux — the canonical grammar accepts
// a SUBSET of valid programs, never invalid ones.
func TestGBNFCanonicalProgramTypeChecks(t *testing.T) {
	// physics in canonical write order (ball_x, ball_y, vel_x, vel_y):
	src := `(cell c (reads ball_x ball_y vel_x vel_y screen_w screen_h) (writes ball_x ball_y vel_x vel_y)
	  (write (ball_x (+ ball_x vel_x)) (ball_y (+ ball_y vel_y))
	         (vel_x (if (or (< ball_x 0) (>= ball_x screen_w)) (neg vel_x) vel_x))
	         (vel_y (if (or (< ball_y 0) (>= ball_y screen_h)) (neg vel_y) vel_y))))`
	if !defaultValid(src, canonLayout) {
		t.Fatal("a canonical-order physics program must type-check")
	}
}

// View cells are unaffected by the canonical-writes knob (they have no writes; draw
// order is semantic and must not be collapsed).
func TestGBNFCanonicalLeavesViewUnchanged(t *testing.T) {
	if GBNFCanonical(canonLayout, KindView) != GBNF(canonLayout, KindView) {
		t.Fatal("canonical-writes must not change the view grammar")
	}
}

package evolution

import (
	"strings"
	"testing"

	"github.com/mdcfrancis/flow/flux"
)

// The no-op seed genome must be stored in the ACTIVE surface: with Forth active, the
// s-expr scaffold renders to a Forth word stream (not "(cell …)") that lowers to the
// IDENTICAL WAT — so a freshly-scaffolded cell is Forth from birth and the synthesis
// loop iterates on a same-surface draft.
func TestActiveSurfaceRendersSeedInForth(t *testing.T) {
	setActiveSurface("forth")
	defer setActiveSurface("sexpr")

	layout := flux.Layout{
		"ball_x":  {Type: flux.TInt, Offset: 0xB0000},
		"ball_vx": {Type: flux.TInt, Offset: 0xB0004},
	}
	sexpr := "(cell c (reads ball_x ball_vx) (writes ball_x) (write (ball_x (+ ball_x ball_vx))))"
	cell, err := (flux.SExpr{}).Read("seed", sexpr, layout)
	if err != nil {
		t.Fatalf("read seed: %v", err)
	}
	forth := ActiveSurface().Render(cell)
	if strings.Contains(forth, "(cell") {
		t.Errorf("Forth-active seed must NOT be s-expr, got: %s", forth)
	}
	// Behavior-invariant: the Forth seed lowers to the same WAT as the s-expr seed.
	wSexpr, err := flux.Compile("c", sexpr, layout)
	if err != nil {
		t.Fatal(err)
	}
	wForth, err := flux.CompileWith(ActiveSurface(), "c", forth, layout)
	if err != nil {
		t.Fatalf("compile forth seed %q: %v", forth, err)
	}
	if wSexpr != wForth {
		t.Errorf("Forth seed must lower to identical WAT as the s-expr seed")
	}
}

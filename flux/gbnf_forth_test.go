package flux

import (
	"strings"
	"testing"
)

func TestGBNFForthShape(t *testing.T) {
	g := GBNFForth(forthLayout, KindCompute)
	for _, want := range []string{"field ::=", "local ::=", "expr0 ::= atom", "binding ::= expr \" =: \" local", "write ::= expr \" -> \" writefield", "root ::="} {
		if !strings.Contains(g, want) {
			t.Fatalf("forth grammar missing %q:\n%s", want, g)
		}
	}
	// Bounded: the deepest expr rule is forthMaxDepth, and it references depth-1.
	if !strings.Contains(g, "expr3 ::=") || strings.Contains(g, "expr4 ::=") {
		t.Fatalf("expected expression depth bounded at %d:\n%s", forthMaxDepth, g)
	}
	// View grammar constrains draw prims, not writes.
	gv := GBNFForth(forthLayout, KindView)
	if !strings.Contains(gv, "circle") || strings.Contains(gv, "writefield") {
		t.Fatalf("view forth grammar should emit draw prims, no writes:\n%s", gv)
	}
}

// Programs shaped like what the grammar produces must be accepted by the Forth
// surface (grammar and reader agree) — including the shallow-stack, prologue-bound
// physics form the depth bound induces.
func TestGBNFForthProgramsAreReadable(t *testing.T) {
	progs := []string{
		`ball_x vel_x + -> ball_x`,
		`ball_x vel_x + =: t0 t0 0 screen_w 1 - clamp -> ball_x`,
		`ball_x vel_x + =: t0 t0 0 < t0 screen_w >= or vel_x neg vel_x ? -> vel_x`,
	}
	for _, p := range progs {
		if _, err := (Forth{}).Read("m", p, forthLayout); err != nil {
			t.Fatalf("grammar-shaped program rejected by reader: %v\n%s", err, p)
		}
	}
}

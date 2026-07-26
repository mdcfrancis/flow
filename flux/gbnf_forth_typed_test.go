package flux

import (
	"strings"
	"testing"
)

func TestGBNFForthTypedStratifies(t *testing.T) {
	g := GBNFForthTyped(forthLayout, KindCompute)
	for _, want := range []string{
		"iexpr0 ::= iatom", "bexpr0 ::= batom",
		`ilocal ::= "i0"`, `blocal ::= "b0"`,
		`binding ::= iexpr " =: " ilocal | bexpr " =: " blocal`,
		`write ::= iexpr " -> " iwrite`,
	} {
		if !strings.Contains(g, want) {
			t.Fatalf("typed grammar missing %q:\n%s", want, g)
		}
	}
	// A write takes an iexpr, never a bexpr — the residual type error made ungrammatical.
	if strings.Contains(g, `write ::= bexpr`) {
		t.Fatal("a bexpr must not be writable to a field")
	}
}

// Grammar-shaped typed programs (Int and Bool locals segregated) are accepted and
// type-check — including the physics shape the typed preamble teaches.
func TestGBNFForthTypedProgramsValid(t *testing.T) {
	progs := []string{
		`ball_x vel_x + -> ball_x`,
		`ball_x vel_x + =: i0 i0 0 screen_w 1 - clamp -> ball_x`,
		`ball_x vel_x + =: i0
		 i0 0 < i0 screen_w >= or =: b0
		 i0 0 screen_w 1 - clamp -> ball_x
		 b0 vel_x neg vel_x ? -> vel_x`,
	}
	for _, p := range progs {
		if _, err := (Forth{}).Read("m", p, forthLayout); err != nil {
			t.Fatalf("typed grammar-shaped program rejected: %v\n%s", err, p)
		}
	}
	// view
	gv := GBNFForthTyped(forthLayout, KindView)
	if !strings.Contains(gv, "circle") || strings.Contains(gv, "iwrite") {
		t.Fatalf("view typed grammar should draw, not write:\n%s", gv)
	}
}

// ForthDiagnose gives explicit, categorized feedback.
func TestForthDiagnose(t *testing.T) {
	if got := ForthDiagnose(`ball_x vel_x + -> ball_x`, forthLayout); got != "ok" {
		t.Fatalf("valid cell should be ok, got %q", got)
	}
	// a type error: writing a Bool (comparison) to an Int field
	if got := ForthDiagnose(`ball_x vel_x < -> ball_x`, forthLayout); !strings.Contains(got, "REJECTED") {
		t.Fatalf("type-mismatched write should be rejected with feedback, got %q", got)
	}
	// a stack/word error: unknown word
	if got := ForthDiagnose(`ball_x frobnicate -> ball_x`, forthLayout); !strings.Contains(got, "stack/word") {
		t.Fatalf("unknown word should be a stack/word rejection, got %q", got)
	}
}

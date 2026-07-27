package flux

import (
	"strings"
	"testing"

	"github.com/mdcfrancis/flow/compiler"
)

func TestExpandBasics(t *testing.T) {
	fields := Layout{
		"orbiter_x":  {Type: TFloat, Offset: 0xB0010},
		"orbiter_vx": {Type: TFloat, Offset: 0xB0018},
		"score":      {Type: TInt, Offset: 0xB0000},
		"grid":       {Type: TInt, Offset: 0xB1000},
	}
	cases := map[string]string{
		"(get orbiter_x)":           "(f32.load (i32.const 0xB0010))",
		"(get score)":               "(i32.load (i32.const 0xB0000))",
		"(set score (i32.const 7))": "(i32.store (i32.const 0xB0000) (i32.const 7))",
		"(field grid)":              "(i32.const 0xB1000)",
	}
	for in, want := range cases {
		got, err := Expand(in, fields)
		if err != nil {
			t.Fatalf("Expand(%q): %v", in, err)
		}
		if got != want {
			t.Errorf("Expand(%q) = %q, want %q", in, got, want)
		}
	}
	// unknown field is an error
	if _, err := Expand("(get nope)", fields); err == nil {
		t.Error("unknown field must error")
	}
}

// The whole point: a FLOAT gravity cell expands to WAT that ASSEMBLES — f32 division
// that would underflow to 0 in integer math is fine in float.
func TestFloatPhysicsExpandsAndAssembles(t *testing.T) {
	fields := Layout{
		"well_x":     {Type: TFloat, Offset: 0xB0000},
		"orbiter_x":  {Type: TFloat, Offset: 0xB0010},
		"orbiter_vx": {Type: TFloat, Offset: 0xB0018},
	}
	src := `(cell run-tick
  ;; vx += G * (well_x - x) / dist   — all f32, no integer underflow
  (set orbiter_vx
    (f32.add (get orbiter_vx)
      (f32.div (f32.mul (f32.const 500000.0) (f32.sub (get well_x) (get orbiter_x)))
               (f32.const 1000000.0))))
  (set orbiter_x (f32.add (get orbiter_x) (get orbiter_vx)))
  (i32.const 0))`
	wat, err := Expand(src, fields)
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	if strings.Contains(wat, "(get ") || strings.Contains(wat, "(set ") || strings.Contains(wat, "(cell ") {
		t.Fatalf("macros left unexpanded:\n%s", wat)
	}
	art, cerr := compiler.NewCompilerService().CompileGenotype(wat)
	if cerr != nil || art == nil || !art.SyntaxPassed {
		t.Fatalf("float physics WAT must assemble: %v\n---\n%s", cerr, wat)
	}
	t.Logf("float physics assembled OK (%d bytes)", len(art.Bytecode))
}

func TestSceneExpandsAndAssembles(t *testing.T) {
	fields := Layout{
		"orbiter_x": {Type: TFloat, Offset: 0xB0010},
		"orbiter_y": {Type: TFloat, Offset: 0xB0014},
	}
	src := `(cell render-frame
  (scene
    (circle (geti orbiter_x) (geti orbiter_y) (i32.const 12) (i32.const 0xFF00FFFF))
    (rect (i32.const 0) (i32.const 0) (i32.const 800) (i32.const 600) (i32.const 0x101018FF))))`
	wat, err := Expand(src, fields)
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	for _, m := range []string{"(scene ", "(circle ", "(geti ", "(cell "} {
		if strings.Contains(wat, m) {
			t.Fatalf("macro %q left unexpanded:\n%s", m, wat)
		}
	}
	// op for a circle on the app layer = (1<<8)|3 = 259; a rect = 257.
	if !strings.Contains(wat, "i32.const 259") || !strings.Contains(wat, "i32.const 257") {
		t.Errorf("draw opcodes missing:\n%s", wat)
	}
	art, cerr := compiler.NewCompilerService().CompileGenotype(wat)
	if cerr != nil || !art.SyntaxPassed {
		t.Fatalf("render WAT must assemble: %v\n---\n%s", cerr, wat)
	}
	t.Logf("render cell assembled OK")
}

func TestFormat(t *testing.T) {
	src := `(cell run-tick (set orb_0_x (get orb_0_x)) (set orb_0_y (get orb_0_y)) (set orb_1_x (i32.add (get orb_1_x) (get orb_1_vx))) (i32.const 0))`
	out := Format(src)
	t.Logf("\n%s", out)
	if !strings.Contains(out, "\n") {
		t.Fatal("long form should be multiline")
	}
	if !strings.Contains(out, "(get orb_0_x)") {
		t.Error("small forms should stay inline")
	}
	// non-sexpr (forth) passes through
	if Format("ball_x ball_vx + -> ball_x") != "ball_x ball_vx + -> ball_x" {
		t.Error("forth should pass through unchanged")
	}
}

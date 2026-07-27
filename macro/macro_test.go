package macro

import (
	"strings"
	"testing"

	"github.com/mdcfrancis/flow/compiler"
)

func TestExpandBasics(t *testing.T) {
	fields := map[string]Field{
		"orbiter_x":  {Offset: 0xB0010, Float: true},
		"orbiter_vx": {Offset: 0xB0018, Float: true},
		"score":      {Offset: 0xB0000, Float: false},
		"grid":       {Offset: 0xB1000, Float: false},
	}
	cases := map[string]string{
		"(get orbiter_x)":         "(f32.load (i32.const 0xB0010))",
		"(get score)":             "(i32.load (i32.const 0xB0000))",
		"(set score (i32.const 7))": "(i32.store (i32.const 0xB0000) (i32.const 7))",
		"(field grid)":            "(i32.const 0xB1000)",
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
	fields := map[string]Field{
		"well_x":     {Offset: 0xB0000, Float: true},
		"orbiter_x":  {Offset: 0xB0010, Float: true},
		"orbiter_vx": {Offset: 0xB0018, Float: true},
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

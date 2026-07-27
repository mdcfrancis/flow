package flux

import (
	"strings"
	"testing"

	"github.com/mdcfrancis/flow/compiler"
)

func ballLayout() Layout {
	return Layout{
		"ball_x":   {Type: TInt, Offset: 0xB0000},
		"vel_x":    {Type: TInt, Offset: 0xB0008},
		"screen_w": {Type: TInt, Offset: 0xB0010},
	}
}

// A cell that DEFINES two composite macros (integrate, reflect) inline and USES them
// must expand to raw WAT that assembles — the whole point of an application prologue.
func TestUserMacroExpandsAndAssembles(t *testing.T) {
	src := `(defmacro (integrate p v) (i32.add (get p) (get v)))
(defmacro (reflect v p lo hi)
  (if (i32.or (i32.lt_s (get p) lo) (i32.ge_s (get p) hi))
    (then (set v (i32.sub (i32.const 0) (get v))))))
(cell run-tick
  (set ball_x (integrate ball_x vel_x))
  (reflect vel_x ball_x (i32.const 0) (get screen_w))
  (i32.const 0))`
	wat, err := Expand(src, ballLayout())
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	if strings.Contains(wat, "(defmacro") || strings.Contains(wat, "(integrate") || strings.Contains(wat, "(reflect") {
		t.Fatalf("macros left in output:\n%s", wat)
	}
	art, cerr := compiler.NewCompilerService().CompileGenotype(wat)
	if cerr != nil || art == nil || !art.SyntaxPassed {
		t.Fatalf("expanded WAT must assemble: %v\n---\n%s", cerr, wat)
	}
}

// A macro used TWICE in one cell expands independently each time (no shared state /
// no collision), and a macro may be passed a full sub-expression as an argument.
func TestMacroUsedTwiceAndSubexprArg(t *testing.T) {
	src := `(defmacro (dbl x) (i32.add x x))
(cell run-tick
  (set ball_x (dbl (get ball_x)))
  (set vel_x (dbl (dbl (get vel_x))))
  (i32.const 0))`
	wat, err := Expand(src, ballLayout())
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	// (dbl (get ball_x)) → (i32.add (i32.load …) (i32.load …))
	if !strings.Contains(wat, "i32.add") {
		t.Fatalf("dbl did not expand:\n%s", wat)
	}
	if _, cerr := compiler.NewCompilerService().CompileGenotype(wat); cerr != nil {
		t.Fatalf("assemble: %v\n%s", cerr, wat)
	}
}

// A macro may call ANOTHER macro; expansion recurses until only built-ins/WAT remain.
func TestMacroCallsMacro(t *testing.T) {
	src := `(defmacro (dbl x) (i32.add x x))
(defmacro (quad x) (dbl (dbl x)))
(cell run-tick (set ball_x (quad (get vel_x))) (i32.const 0))`
	wat, err := Expand(src, ballLayout())
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	if strings.Contains(wat, "quad") || strings.Contains(wat, "dbl") {
		t.Fatalf("nested macro left unexpanded:\n%s", wat)
	}
}

// A wrong argument count is a clear expand-time error (fed back to the correction loop).
func TestMacroArgCountMismatch(t *testing.T) {
	src := `(defmacro (reflect v p lo hi) (get p))
(cell run-tick (set ball_x (reflect vel_x ball_x)) (i32.const 0))`
	if _, err := Expand(src, ballLayout()); err == nil {
		t.Fatal("calling a 4-arg macro with 2 args must error")
	}
}

// A template that declares a (local …) is rejected at definition time (v1 hygiene rule).
func TestMacroDeclaringLocalRejected(t *testing.T) {
	src := `(defmacro (bad x) (local $t i32))
(cell run-tick (set ball_x (get ball_x)) (i32.const 0))`
	if _, err := Expand(src, ballLayout()); err == nil {
		t.Fatal("a macro template declaring a local must be rejected")
	}
}

// A self-referential macro cannot loop forever — expansion is depth-bounded.
func TestMacroRecursionCapped(t *testing.T) {
	src := `(defmacro (loop x) (loop x))
(cell run-tick (set ball_x (loop (get ball_x))) (i32.const 0))`
	if _, err := Expand(src, ballLayout()); err == nil {
		t.Fatal("a recursive macro must hit the depth cap and error")
	}
}

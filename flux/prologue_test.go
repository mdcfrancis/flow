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

// HYGIENE: a template may not REFERENCE any $-local (a local.get/set/tee), so a macro
// can never read or clobber a caller's local. This is the capture guard.
func TestMacroTemplateLocalReferenceRejected(t *testing.T) {
	for _, tmpl := range []string{
		"(defmacro (bad x) (local.get $t))",
		"(defmacro (bad x) (i32.add x (local.get $tmp)))",
		"(defmacro (bad x) (local.set $t x))",
	} {
		src := tmpl + "\n(cell run-tick (set ball_x (bad (get ball_x))) (i32.const 0))"
		if _, err := Expand(src, ballLayout()); err == nil {
			t.Fatalf("a template referencing a $-local must be rejected: %s", tmpl)
		}
	}
}

// HYGIENE, the other side: passing a caller's $-local AS AN ARGUMENT is fine — the
// arg is substituted into the (clean) template, it is not the template referencing a
// local. This is the template/argument distinction the live physics cell relies on.
func TestMacroArgumentLocalIsAllowed(t *testing.T) {
	src := `(defmacro (clampi x lo hi) (select (select x hi (i32.lt_s x hi)) lo (i32.gt_s lo (select x hi (i32.lt_s x hi)))))
(cell run-tick
  (local $nx i32)
  (local.set $nx (i32.add (get ball_x) (get vel_x)))
  (set ball_x (clampi (local.get $nx) (i32.const 0) (get screen_w)))
  (i32.const 0))`
	wat, err := Expand(src, ballLayout())
	if err != nil {
		t.Fatalf("passing a $-local as an argument must be allowed: %v", err)
	}
	if _, cerr := compiler.NewCompilerService().CompileGenotype(wat); cerr != nil {
		t.Fatalf("assemble: %v\n%s", cerr, wat)
	}
}

// HYGIENE: a macro name or parameter may not shadow a built-in head or a bare WAT
// keyword — substituting/shadowing one would corrupt an expansion.
func TestMacroReservedNamesRejected(t *testing.T) {
	cases := []string{
		"(defmacro (get x) x)",       // name shadows the field-read primitive
		"(defmacro (set x) x)",       // name shadows the field-write primitive
		"(defmacro (f if) (get if))", // parameter shadows the WAT (if …) keyword
		"(defmacro (f select) select)",
		"(defmacro (f x x) (i32.add x x))", // duplicate parameter
	}
	for _, def := range cases {
		src := def + "\n(cell run-tick (i32.const 0))"
		if _, err := Expand(src, ballLayout()); err == nil {
			t.Fatalf("must be rejected: %s", def)
		}
	}
}

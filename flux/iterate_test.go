package flux

import (
	"strings"
	"testing"

	"github.com/mdcfrancis/flow/compiler"
)

// The whole point of the substrate: a RENDER cell that iterates a particle array and
// draws one circle per element — the thing (scene)'s fixed prim list could never express.
// It must expand (loop + per-element (draw) records) and ASSEMBLE.
func TestForDrawLoopRenders(t *testing.T) {
	layout := Layout{
		"count": {Type: TInt, Offset: 0xB0000},
		"px":    {Type: TBuffer, Offset: 0xB0100, Len: 64},
		"py":    {Type: TBuffer, Offset: 0xB0200, Len: 64},
	}
	src := `(cell render-frame
  (for $i (get count)
    (draw (circle (atidx px $i) (atidx py $i) (i32.const 3) (i32.const 0xFFCC33FF)))))`
	wat, err := Expand(src, layout)
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	// No macro sugar should survive: no (for/(draw/(scene/(atidx/(circle.
	for _, m := range []string{"(for ", "(draw ", "(scene ", "(atidx ", "(circle ", "(cell "} {
		if strings.Contains(wat, m) {
			t.Fatalf("macro %q left unexpanded:\n%s", m, wat)
		}
	}
	// It must actually be a loop that stores draw records.
	if !strings.Contains(wat, "loop") || !strings.Contains(wat, "br_if") || !strings.Contains(wat, "i32.store") {
		t.Fatalf("expected a loop emitting records:\n%s", wat)
	}
	art, cerr := compiler.NewCompilerService().CompileGenotype(wat)
	if cerr != nil || art == nil || !art.SyntaxPassed {
		t.Fatalf("looped renderer must assemble: %v\n---\n%s", cerr, wat)
	}
}

// (for) works in a compute cell too: double every element of a buffer in place.
func TestForLoopCompute(t *testing.T) {
	layout := Layout{
		"n":   {Type: TInt, Offset: 0xB0000},
		"buf": {Type: TBuffer, Offset: 0xB0100, Len: 64},
	}
	src := `(cell run-tick
  (for $i (get n)
    (setidx buf $i (i32.mul (atidx buf $i) (i32.const 2))))
  (i32.const 0))`
	wat, err := Expand(src, layout)
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	if _, cerr := compiler.NewCompilerService().CompileGenotype(wat); cerr != nil {
		t.Fatalf("compute for-loop must assemble: %v\n%s", cerr, wat)
	}
}

// Nested (for) loops get distinct labels (hygienic gensym) and assemble — a grid draw.
func TestNestedForRenders(t *testing.T) {
	layout := Layout{"cols": {Type: TInt, Offset: 0xB0000}, "rows": {Type: TInt, Offset: 0xB0004}}
	src := `(cell render-frame
  (for $y (get rows)
    (for $x (get cols)
      (draw (rect (i32.mul (local.get $x) (i32.const 10)) (i32.mul (local.get $y) (i32.const 10))
                  (i32.const 8) (i32.const 8) (i32.const 0xFF3366FF))))))`
	wat, err := Expand(src, layout)
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	if _, cerr := compiler.NewCompilerService().CompileGenotype(wat); cerr != nil {
		t.Fatalf("nested for render must assemble: %v\n%s", cerr, wat)
	}
}

// A static (scene …) still works: it is now sugar over (draw …) under the render
// accumulator, so existing render genomes keep assembling and emit the same records.
func TestSceneStillAccumulates(t *testing.T) {
	layout := Layout{"ball_x": {Type: TInt, Offset: 0xB0000}, "ball_y": {Type: TInt, Offset: 0xB0004}}
	src := `(cell render-frame
  (scene (circle (get ball_x) (get ball_y) (i32.const 8) (i32.const 0xFFCC33FF))
         (rect (i32.const 0) (i32.const 0) (i32.const 320) (i32.const 240) (i32.const 0x101018FF))))`
	wat, err := Expand(src, layout)
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	if !strings.Contains(wat, "i32.const 259") || !strings.Contains(wat, "i32.const 257") {
		t.Errorf("draw opcodes missing (circle=259, rect=257):\n%s", wat)
	}
	if _, cerr := compiler.NewCompilerService().CompileGenotype(wat); cerr != nil {
		t.Fatalf("scene sugar must assemble: %v\n%s", cerr, wat)
	}
}

// A combinator LEAF authors physics over ITS ELEMENT: (get/set NAME) on an Elem field
// read/write argPtr+offset (this particle), not the global array. Gravity toward a
// GLOBAL attractor + in-place integrate, in macro-WAT — the thing a leaf could not do
// when handed the global contract layout (it fell to raw-WAT render code).
func TestLeafElementLayout(t *testing.T) {
	layout := Layout{
		// element fields (arg-pointer-relative): this particle's x,y,vx,vy
		"x":  {Type: TFloat, Offset: 0, Elem: true},
		"y":  {Type: TFloat, Offset: 4, Elem: true},
		"vx": {Type: TFloat, Offset: 8, Elem: true},
		"vy": {Type: TFloat, Offset: 12, Elem: true},
		// global fields (absolute): the shared attractor
		"attractor_x": {Type: TFloat, Offset: 0xB0000},
		"attractor_y": {Type: TFloat, Offset: 0xB0004},
	}
	src := `(cell run-tick
  (set vx (f32.add (get vx) (f32.div (f32.sub (get attractor_x) (get x)) (f32.const 100.0))))
  (set vy (f32.add (get vy) (f32.div (f32.sub (get attractor_y) (get y)) (f32.const 100.0))))
  (set x (f32.add (get x) (get vx)))
  (set y (f32.add (get y) (get vy)))
  (i32.const 0))`
	wat, err := Expand(src, layout)
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	// Element reads/writes must be argPtr-relative: (i32.add (local.get 0) (i32.const …)).
	if !strings.Contains(wat, "(local.get 0)") {
		t.Fatalf("element access is not arg-pointer-relative:\n%s", wat)
	}
	// The global attractor must stay absolute.
	if !strings.Contains(wat, "0xB0000") {
		t.Fatalf("global attractor field not absolute:\n%s", wat)
	}
	if _, cerr := compiler.NewCompilerService().CompileGenotype(wat); cerr != nil {
		t.Fatalf("leaf element physics must assemble: %v\n%s", cerr, wat)
	}
}

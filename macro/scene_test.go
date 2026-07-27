package macro

import (
	"strings"
	"testing"

	"github.com/mdcfrancis/flow/compiler"
)

func TestSceneExpandsAndAssembles(t *testing.T) {
	fields := map[string]Field{
		"orbiter_x": {Offset: 0xB0010, Float: true},
		"orbiter_y": {Offset: 0xB0014, Float: true},
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

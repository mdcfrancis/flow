package evolution

import (
	"strings"
	"testing"

	"github.com/mdcfrancis/flow/compiler"
)

func TestAgenticToolsExec(t *testing.T) {
	le := kbLedger(t)
	if err := AddDocument(le, Document{Topic: "draw-at-position", Title: "Draw at a field",
		Kinds: []string{"render"}, Body: "read the field and draw there", Provenance: "seed"}); err != nil {
		t.Fatal(err)
	}
	_, exec := buildAgenticTools(le, compiler.NewCompilerService(), "render", "draw a circle")

	// find_docs surfaces the relevant doc body.
	if out := exec("find_docs", `{"query":"draw"}`); !strings.Contains(out, "draw there") {
		t.Fatalf("find_docs returned %q", out)
	}
	// compile_check on valid WAT → ok.
	good := `{"wat":"(module (func (export \"run-tick\") (param i32 i32) (result i32) i32.const 0))"}`
	if out := exec("compile_check", good); !strings.Contains(out, "ok") {
		t.Fatalf("compile_check(good) = %q", out)
	}
	// compile_check on broken WAT → the exact assembler error, fed back to the model.
	if out := exec("compile_check", `{"wat":"(module (this is not valid"}`); !strings.Contains(strings.ToUpper(out), "COMPILE ERROR") {
		t.Fatalf("compile_check(bad) = %q, want a COMPILE ERROR", out)
	}
	// unknown tool name is reported, not panicked.
	if out := exec("nope", `{}`); !strings.Contains(out, "unknown tool") {
		t.Fatalf("unknown tool = %q", out)
	}
}

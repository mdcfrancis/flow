package evolution

import (
	"context"
	"testing"

	"github.com/mdcfrancis/flow/compiler"
)

// TestBuildExamplesCompile guards that the few-shot examples embedded in the
// builder prompt are actually valid WAT that satisfies their entry contracts —
// we must never feed the model broken examples.
func TestBuildExamplesCompile(t *testing.T) {
	cs := compiler.NewCompilerService()
	cases := []struct {
		name     string
		wat      string
		contract *EntryContract
	}{
		{"run-tick", exampleRunTickWAT, RunTickContract},
		{"render-frame", exampleRenderFrameWAT, RenderFrameContract},
	}
	for _, c := range cases {
		art, err := cs.CompileGenotype(c.wat)
		if err != nil || art == nil || !art.SyntaxPassed {
			t.Fatalf("%s example failed to compile: %v (%s)", c.name, err, artErr(art))
		}
		if sigErr := checkEntrySignature(context.Background(), art.Bytecode, c.contract); sigErr != nil {
			t.Fatalf("%s example violates its contract: %v", c.name, sigErr)
		}
	}
}

func artErr(a *compiler.CompilationArtifact) string {
	if a == nil {
		return ""
	}
	return a.ErrorContext
}

package evolution

import (
	"strings"
	"testing"

	"github.com/mdcfrancis/flow/compiler"
	"github.com/mdcfrancis/flow/flux"
	"github.com/mdcfrancis/flow/stdlib"
)

// Every macro seed example must EXPAND against the representative seed layout and ASSEMBLE
// — a broken example is worse than none (it poisons retrieval and teaches the wrong shape).
// This guards the collection-loop physics example (and all the others) at build time,
// not just silently-skipped at seed time.
func TestSeedMacroExamplesExpandAndAssemble(t *testing.T) {
	svc := compiler.NewCompilerService()
	sawGravity := false
	for _, se := range stdlib.Examples() {
		if !se.IsMacro {
			continue
		}
		if strings.Contains(strings.Join(se.Tags, " "), "gravity") || strings.Contains(se.Semantics, "gravitational") {
			sawGravity = true
		}
		wat, err := flux.Expand(se.Src, seedMacroLayout)
		if err != nil {
			t.Errorf("macro example %q did not expand: %v", se.Semantics, err)
			continue
		}
		if art, cerr := svc.CompileGenotype(wat); cerr != nil || art == nil || !art.SyntaxPassed {
			t.Errorf("macro example %q did not assemble: %v", se.Semantics, cerr)
		}
	}
	if !sawGravity {
		t.Error("expected the particle-gravity collection-loop example to be present")
	}
}

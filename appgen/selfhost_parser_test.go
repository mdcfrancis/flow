package appgen

import (
	"context"
	"testing"

	"github.com/mdcfrancis/flow/compiler"
	"github.com/mdcfrancis/flow/evolution"
	"github.com/mdcfrancis/flow/execution"
)

// The behavior-pinning suites must actually PASS against the compiled self-hosted
// parser cells — otherwise enrolling them would immediately stall evolution. Compile
// each cell and score its suite; every scenario must hold.
func TestSelfHostParserAcceptancePasses(t *testing.T) {
	lexWAT, reduceWAT, err := execution.CompileSelfHostParser()
	if err != nil {
		t.Fatalf("compile self-host parser: %v", err)
	}
	cs := compiler.NewCompilerService()
	wasm := func(wat string) []byte {
		art, cerr := cs.CompileGenotype(wat)
		if cerr != nil || !art.SyntaxPassed {
			t.Fatalf("wasm compile: %v (%s)", cerr, art.ErrorContext)
		}
		return art.Bytecode
	}
	bc := map[string][]byte{
		execution.SelfHostLexerURN:  wasm(lexWAT),
		execution.SelfHostParserURN: wasm(reduceWAT),
	}
	ctx := context.Background()
	for urn, suite := range SelfHostParserAcceptance() {
		p, total := evolution.ScoreSuite(ctx, bc[urn], "run-tick", suite,
			evolution.DefaultPayloadOffset, evolution.DefaultStateWindow, nil)
		if total == 0 {
			t.Fatalf("%s: suite has no checks", urn)
		}
		if p != total {
			t.Errorf("%s: acceptance %d/%d — cells must fully pass their behavior pin", urn, p, total)
		}
	}
}

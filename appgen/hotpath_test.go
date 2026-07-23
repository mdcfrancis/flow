package appgen

import (
	"context"
	"testing"

	"github.com/mdcfrancis/flow/compiler"
	"github.com/mdcfrancis/flow/evolution"
)

func TestHotpathBaselinePasses(t *testing.T) {
	cs := compiler.NewCompilerService()
	path, err := cs.CompileGenotype(HotpathWAT)
	if err != nil || !path.SyntaxPassed {
		t.Fatalf("compile hotpath: %v (%s)", err, path.ErrorContext)
	}
	leaf, err := cs.CompileGenotype(HotleafWAT)
	if err != nil || !leaf.SyntaxPassed {
		t.Fatalf("compile hotleaf: %v (%s)", err, leaf.ErrorContext)
	}
	// The dispatch resolves demo:hotleaf through the resolver — the non-inlinable cost.
	resolver := func(urn string) ([]byte, bool) {
		if urn == HotleafURN {
			return leaf.Bytecode, true
		}
		return nil, false
	}
	suite := HotpathAcceptance(evolution.DefaultPayloadOffset)
	p, total := evolution.ScoreSuite(context.Background(), path.Bytecode, "run-tick", suite,
		evolution.DefaultPayloadOffset, evolution.DefaultStateWindow, resolver)
	if total == 0 || p != total {
		t.Fatalf("baseline hotpath must pass its acceptance (dispatch to hotleaf), got %d/%d", p, total)
	}
}

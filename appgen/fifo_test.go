package appgen

import (
	"context"
	"testing"

	"github.com/mdcfrancis/flow/compiler"
	"github.com/mdcfrancis/flow/evolution"
)

func TestFifoBaselinePasses(t *testing.T) {
	art, err := compiler.NewCompilerService().CompileGenotype(FifoWAT)
	if err != nil || !art.SyntaxPassed {
		t.Fatalf("compile fifo: %v (%s)", err, art.ErrorContext)
	}
	suite := FifoAcceptance(evolution.DefaultPayloadOffset)
	p, total := evolution.ScoreSuite(context.Background(), art.Bytecode, "run-tick", suite,
		evolution.DefaultPayloadOffset, evolution.DefaultStateWindow, nil)
	if total == 0 || p != total {
		t.Fatalf("baseline fifo must pass its acceptance, got %d/%d", p, total)
	}
}

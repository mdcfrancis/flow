package evolution

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/mdcfrancis/flow/inference"
)

// toolModelStub does its real work in a tool call (flux_run) and then ends with a
// prose summary carrying no (cell …) — the exact behavior that produced "empty
// source stream detected". The agentic sieve must fall back to the last
// tool-verified program rather than discard it.
type toolModelStub struct{ src string }

func (m toolModelStub) InvokeTools(_ context.Context, _, _ string, _ []inference.ToolDef, exec inference.ToolExec, _ int) (string, error) {
	// The model verifies its cell empirically, then answers in prose.
	exec("flux_run", fmt.Sprintf(`{"src": %q, "inputs": "{\"ball_x\":10,\"vel_x\":2,\"screen_w\":320,\"screen_h\":240}"}`, m.src))
	return "I verified the cell with flux_run and it behaves correctly. Done.", nil
}

func TestAgenticFallsBackToToolVerifiedFlux(t *testing.T) {
	le := kbLedger(t)
	out, err := RunAgenticSieve(context.Background(), toolModelStub{fluxPhysics}, le,
		"sys", "seed", "", "bounce", fluxBallLayout(), RunTickContract)
	if err != nil {
		t.Fatalf("agentic sieve discarded the tool-verified program: %v", err)
	}
	if out.Flux == "" || !strings.Contains(out.Flux, "(cell physics") {
		t.Fatalf("expected the tool-verified Flux as the genome, got: %q", out.Flux)
	}
	// The recovered cell is real and correct through the operational harness.
	pass, total := ScenarioScore(context.Background(), out.Artifact.Bytecode, physicsScenarios(), DefaultPayloadOffset, DefaultStateWindow, nil)
	if pass != total {
		t.Fatalf("recovered cell passed only %d/%d scenarios", pass, total)
	}
}

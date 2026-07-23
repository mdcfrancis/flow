package appgen

import (
	"context"
	"strings"
	"testing"

	"github.com/mdcfrancis/flow/evolution"
)

// planModel returns a plan JSON whose component reads/writes DISAGREE with the
// envelope — the grounding must override them with the declared ports.
type planModel struct{}

func (planModel) InvokeReasoning(ctx context.Context, sys, user string) (string, error) {
	return `{"overview":"input writes player_x; view draws it",
	  "choreography":["input updates player_x","view draws player at player_x"],
	  "components":[
	    {"identity":"urn:hdm:apps:si:input","purpose":"move player",
	     "reads":["WRONG"],"writes":["ALSO_WRONG"],
	     "steps":["read HMI key","on d, player_x += 5, clamp"],
	     "interactions":["writes player_x, read by view"]},
	    {"identity":"urn:hdm:apps:si:view","purpose":"draw",
	     "steps":["read player_x","draw a rect at player_x"]}]}`, nil
}

func TestAuthorPlanGroundsPortsFromEnvelope(t *testing.T) {
	g, _ := newGrower(t, planModel{})
	saveEnvelope(g.ledger, &AppEnvelope{
		ApplicationNamespace: "urn:hdm:apps:si", Objective: "move a dot",
		SubsystemRequirements: []Subsystem{
			{Identity: "urn:hdm:apps:si:input", Semantics: "read input, move player",
				Reads: []string{"HMI input"}, Writes: []string{"player_x"}},
			{Identity: "urn:hdm:apps:si:view", Semantics: "render the scene",
				Reads: []string{"player_x"}},
		},
	})
	if err := g.AuthorPlan(context.Background(), "urn:hdm:apps:si"); err != nil {
		t.Fatalf("AuthorPlan: %v", err)
	}
	p := evolution.LoadPlan(g.ledger, "urn:hdm:apps:si")
	if p == nil || len(p.Components) != 2 {
		t.Fatalf("plan = %+v", p)
	}
	in := p.Component("urn:hdm:apps:si:input")
	// Ports are the DECLARED ones, not the model's "WRONG"/"ALSO_WRONG".
	if len(in.Reads) != 1 || in.Reads[0] != "HMI input" || len(in.Writes) != 1 || in.Writes[0] != "player_x" {
		t.Fatalf("ports not grounded from envelope: reads=%v writes=%v", in.Reads, in.Writes)
	}
	// The model-designed algorithm survives.
	if len(in.Steps) == 0 || !strings.Contains(strings.Join(in.Steps, " "), "player_x") {
		t.Fatalf("steps not preserved: %v", in.Steps)
	}
	// Idempotent: a second call authors nothing new.
	if err := g.AuthorPlan(context.Background(), "urn:hdm:apps:si"); err != nil {
		t.Fatalf("second AuthorPlan: %v", err)
	}
}

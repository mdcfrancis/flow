package appgen

import (
	"context"
	"testing"
)

func TestChallengeArchitectureAddsMissingSubsystem(t *testing.T) {
	// Critic proposes a missing subsystem; the rest of the model traffic
	// (wit/genesis/acceptance during scaffold) is satisfied by goodSkeleton.
	g, repo := newGrower(t, &seqModel{resp: []string{
		`{"complete":false,"subsystem":{"identity":"urn:hdm:apps:x:extra","semantics":"handle the missing concern"}}`,
		goodSkeleton,
	}})
	saveEnvelope(g.ledger, &AppEnvelope{
		ApplicationNamespace:  "urn:hdm:apps:x",
		Objective:             "build the x application",
		SubsystemRequirements: []Subsystem{{Identity: "urn:hdm:apps:x:core", Semantics: "core logic"}},
	})

	var enrolled []string
	added, err := g.ChallengeArchitecture(context.Background(), "urn:hdm:apps:x", func(u string) { enrolled = append(enrolled, u) })
	if err != nil {
		t.Fatalf("challenge: %v", err)
	}
	if added != 1 {
		t.Fatalf("added = %d, want 1", added)
	}
	if len(enrolled) != 1 || enrolled[0] != "urn:hdm:apps:x:extra" {
		t.Fatalf("enrolled = %v, want [urn:hdm:apps:x:extra]", enrolled)
	}
	// Envelope grew and the new cell is live.
	env := LoadEnvelope(g.ledger, "urn:hdm:apps:x")
	if env == nil || len(env.SubsystemRequirements) != 2 {
		t.Fatalf("envelope not extended: %+v", env)
	}
	if _, err := repo.Load("urn:hdm:apps:x:extra"); err != nil {
		t.Fatalf("proposed subsystem not scaffolded: %v", err)
	}
}

func TestChallengeArchitectureCompleteIsNoop(t *testing.T) {
	g, _ := newGrower(t, &seqModel{resp: []string{`{"complete":true}`}})
	saveEnvelope(g.ledger, &AppEnvelope{
		ApplicationNamespace:  "urn:hdm:apps:done",
		Objective:             "a finished app",
		SubsystemRequirements: []Subsystem{{Identity: "urn:hdm:apps:done:core", Semantics: "everything"}},
	})
	added, err := g.ChallengeArchitecture(context.Background(), "urn:hdm:apps:done", nil)
	if err != nil || added != 0 {
		t.Fatalf("complete challenge: added=%d err=%v, want 0/nil", added, err)
	}
}

func TestChallengeArchitectureNoEnvelopeIsNoop(t *testing.T) {
	g, _ := newGrower(t, &seqModel{resp: []string{`{"complete":false,"subsystem":{"identity":"a","semantics":"b"}}`}})
	added, err := g.ChallengeArchitecture(context.Background(), "urn:hdm:apps:ghost", nil)
	if err != nil || added != 0 {
		t.Fatalf("no-envelope challenge: added=%d err=%v, want 0/nil", added, err)
	}
}

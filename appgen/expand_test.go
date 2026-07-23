package appgen

import (
	"context"
	"testing"

	"github.com/mdcfrancis/flow/compiler"
	"github.com/mdcfrancis/flow/evolution"
	"github.com/mdcfrancis/flow/manifest"
)

// seqModel returns canned completions in order, repeating the last.
type seqModel struct {
	resp []string
	i    int
}

func (m *seqModel) InvokeReasoning(ctx context.Context, sys, user string) (string, error) {
	r := m.resp[m.i]
	if m.i < len(m.resp)-1 {
		m.i++
	}
	return r, nil
}

func TestExpandSuiteAddsOnlyFaithfulChecks(t *testing.T) {
	// Call 1 (propose): a good new test and a hallucinated one.
	// Call 2 (validate): approve the good, reject the bad; the pre-existing check
	// is omitted (=> retained).
	g, repo := newGrower(t, &seqModel{resp: []string{
		`{"tests":[{"name":"d4","input":4,"expected":8},{"name":"bad","input":5,"expected":99}]}`,
		`{"verdicts":[{"name":"d4","faithful":true,"reason":"4*2=8"},{"name":"bad","faithful":false,"reason":"5*2=10, not 99"}]}`,
	}})

	const urn = "urn:hdm:apps:x:doubler"
	art, err := compiler.NewCompilerService().CompileGenotype(goodSkeleton)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	h, _, err := repo.PutCell(urn, goodSkeleton, art.Bytecode,
		manifest.SemanticManifest{FunctionalIntent: "double the input"}, 0)
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := repo.SeedRef(urn, h); err != nil {
		t.Fatalf("seed ref: %v", err)
	}
	// Existing suite: one check.
	if err := evolution.SaveAcceptance(g.ledger, urn,
		&evolution.AcceptanceSuite{Tests: []evolution.AcceptanceTest{{Name: "d2", Input: 2, Expected: 4}}}); err != nil {
		t.Fatalf("save existing: %v", err)
	}

	added, err := g.ExpandSuite(context.Background(), urn)
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	if added != 1 {
		t.Fatalf("added = %d, want 1 (d4 certified, bad rejected)", added)
	}
	got, _ := evolution.LoadAcceptance(g.ledger, urn)
	names := map[string]bool{}
	for _, tc := range got.Tests {
		names[tc.Name] = true
	}
	if !names["d2"] || !names["d4"] || names["bad"] {
		t.Fatalf("suite = %v, want {d2,d4} and NOT bad", names)
	}
}

func TestExpandSuiteNoRequirementIsNoop(t *testing.T) {
	g, repo := newGrower(t, &seqModel{resp: []string{`{}`}})
	const urn = "urn:hdm:apps:x:blank"
	art, _ := compiler.NewCompilerService().CompileGenotype(goodSkeleton)
	h, _, _ := repo.PutCell(urn, goodSkeleton, art.Bytecode, manifest.SemanticManifest{}, 0)
	_ = repo.SeedRef(urn, h)
	added, err := g.ExpandSuite(context.Background(), urn)
	if err != nil || added != 0 {
		t.Fatalf("no-requirement expand: added=%d err=%v, want 0/nil", added, err)
	}
}

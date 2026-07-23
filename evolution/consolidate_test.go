package evolution

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mdcfrancis/flow/manifest"
	"github.com/mdcfrancis/flow/storage"
)

func consolidateOrch(t *testing.T, model Reasoner) (*Orchestrator, *manifest.Repository) {
	t.Helper()
	le, err := storage.NewLedgerEngine(filepath.Join(t.TempDir(), "hdm.db"))
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	t.Cleanup(func() { le.Close() })
	return NewOrchestrator(le, model), manifest.NewRepository(le)
}

func TestConsolidationCandidateProposesValidPair(t *testing.T) {
	orch, repo := consolidateOrch(t, &fakeReasoner{responses: []string{
		`{"consolidate":true,"target":"urn:hdm:apps:x:a","partner":"urn:hdm:apps:x:b","reason":"both tally scores"}`,
	}})
	wat := `(module ` + sharedImport + `
	  (func (export "run-tick") (param i32 i32) (result i32) i32.const 0))`
	seed(t, repo, "urn:hdm:apps:x:a", wat)
	seed(t, repo, "urn:hdm:apps:x:b", wat)

	target, partner, _, err := orch.ConsolidationCandidate(context.Background(),
		[]string{"urn:hdm:apps:x:a", "urn:hdm:apps:x:b"})
	if err != nil {
		t.Fatalf("candidate: %v", err)
	}
	if target != "urn:hdm:apps:x:a" || partner != "urn:hdm:apps:x:b" {
		t.Fatalf("pair = (%q,%q), want (a,b)", target, partner)
	}
}

func TestConsolidationCandidateRejectsInvalidOrNone(t *testing.T) {
	// "no consolidation" verdict.
	orch, repo := consolidateOrch(t, &fakeReasoner{responses: []string{`{"consolidate":false}`}})
	wat := `(module ` + sharedImport + `
	  (func (export "run-tick") (param i32 i32) (result i32) i32.const 0))`
	seed(t, repo, "urn:hdm:apps:x:a", wat)
	seed(t, repo, "urn:hdm:apps:x:b", wat)
	if tg, _, _, _ := orch.ConsolidationCandidate(context.Background(), []string{"urn:hdm:apps:x:a", "urn:hdm:apps:x:b"}); tg != "" {
		t.Fatalf("expected no pair, got %q", tg)
	}

	// A pair referencing a cell not in the input set is rejected.
	orch2, repo2 := consolidateOrch(t, &fakeReasoner{responses: []string{
		`{"consolidate":true,"target":"urn:hdm:apps:x:a","partner":"urn:hdm:apps:x:ghost"}`,
	}})
	seed(t, repo2, "urn:hdm:apps:x:a", wat)
	seed(t, repo2, "urn:hdm:apps:x:b", wat)
	if tg, _, _, _ := orch2.ConsolidationCandidate(context.Background(), []string{"urn:hdm:apps:x:a", "urn:hdm:apps:x:b"}); tg != "" {
		t.Fatalf("invalid pair should be rejected, got %q", tg)
	}
}

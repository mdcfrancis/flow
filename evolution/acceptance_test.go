package evolution

import (
	"context"
	"testing"

	"github.com/mdcfrancis/flow/compiler"
	"github.com/mdcfrancis/flow/manifest"
)

func TestAcceptanceFrameCommitsCorrectnessProgress(t *testing.T) {
	ctx := context.Background()
	le, repo := topoLedger(t)

	const urn = "urn:hdm:cell:acc"
	// Baseline always returns 0.
	seed(t, repo, urn, `(module `+sharedImport+`
	  (func (export "run-tick") (param i32 i32) (result i32) i32.const 0))`)

	// Spec suite: input 1 must yield 1, input 0 must yield 0. The baseline
	// scores 1/2 (only the zero case); an identity implementation scores 2/2.
	suite := &AcceptanceSuite{Tests: []AcceptanceTest{
		{Name: "one", Input: 1, Expected: 1},
		{Name: "zero", Input: 0, Expected: 0},
	}}
	if err := SaveAcceptance(le, urn, suite); err != nil {
		t.Fatalf("save acceptance: %v", err)
	}

	// The model returns an identity cell (reads the 4-byte input and returns it).
	identity := `(module ` + sharedImport + `
	  (func (export "run-tick") (param $p i32) (param $len i32) (result i32)
	    local.get $p i32.load))`
	orch := NewOrchestrator(le, &fakeReasoner{responses: []string{identity}})

	fr, err := orch.RunFrame(ctx, urn)
	if err != nil {
		t.Fatalf("run frame: %v", err)
	}
	if fr.AcceptTotal != 2 || fr.AcceptBase != 1 {
		t.Fatalf("expected baseline 1/2, got %d/%d", fr.AcceptBase, fr.AcceptTotal)
	}
	if fr.AcceptCand != 2 {
		t.Fatalf("candidate should score 2/2, got %d", fr.AcceptCand)
	}
	if !fr.Committed {
		t.Fatalf("correctness progress must be committed: %s", fr.Reason)
	}

	// A regression candidate (always 1) scores 1/2 -> must be rejected.
	regress := `(module ` + sharedImport + `
	  (func (export "run-tick") (param i32 i32) (result i32) i32.const 1))`
	// Re-seed baseline as the now-correct identity so base=2/2.
	idArt, _ := compiler.NewCompilerService().CompileGenotype(identity)
	h, _, _ := repo.PutCell(urn, identity, idArt.Bytecode, manifest.SemanticManifest{}, 0)
	repo.SeedRef(urn, h)
	orch2 := NewOrchestrator(le, &fakeReasoner{responses: []string{regress}})
	fr2, err := orch2.RunFrame(ctx, urn)
	if err != nil {
		t.Fatalf("run frame 2: %v", err)
	}
	if fr2.Committed {
		t.Fatalf("acceptance regression must not commit (%d->%d/%d)", fr2.AcceptBase, fr2.AcceptCand, fr2.AcceptTotal)
	}
}

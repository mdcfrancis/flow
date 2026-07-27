package evolution

import (
	"context"
	"testing"
)

func TestReauthorVerdict(t *testing.T) {
	if g, f := reauthorVerdict(2, 2); !g || f != 1 {
		t.Fatalf("all passing → green, fitness 1; got %v %v", g, f)
	}
	if g, f := reauthorVerdict(1, 2); g || f != 0.5 {
		t.Fatalf("partial → not green, fitness 0.5; got %v %v", g, f)
	}
	if g, f := reauthorVerdict(0, 0); g || f != 0 {
		t.Fatalf("no scenarios → not green, fitness 0; got %v %v", g, f)
	}
}

// The full macro loop, deterministically (fake model): a cell recorded under the old
// language is stale under a new one; ReauthorCell drives a real build frame that
// takes it green; the gate promotes because it came back green and fitness improved,
// and the live version is bumped with the new genome kept.
func TestProposeLanguageChangePromotesViaReauthor(t *testing.T) {
	ctx := context.Background()
	le, repo := topoLedger(t)

	const urn = "urn:hdm:cell:reauth"
	seed(t, repo, urn, `(module `+sharedImport+`
	  (func (export "run-tick") (param i32 i32) (result i32) i32.const 0))`) // baseline 1/2
	suite := &AcceptanceSuite{Tests: []AcceptanceTest{
		{Name: "one", Input: 1, Expected: 1},
		{Name: "zero", Input: 0, Expected: 0},
	}}
	if err := SaveAcceptance(le, urn, suite); err != nil {
		t.Fatal(err)
	}
	// The cell was authored under the old language version.
	if err := RecordLineage(le, Lineage{Result: "g-old", URN: urn, Language: FluxLanguageVersion}); err != nil {
		t.Fatal(err)
	}

	identity := `(module ` + sharedImport + `
	  (func (export "run-tick") (param $p i32) (param $len i32) (result i32) local.get $p i32.load))`
	orch := NewOrchestrator(le, &fakeReasoner{responses: []string{identity}})

	rep, err := ProposeLanguageChange(le, "flux/v2", 0.4,
		orch.ReauthorCell(ctx, 3),
		func(outs []RebuildOutcome) float64 { return 1.0 }, // candidate scoreboard fitness
	)
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	if !rep.AllGreen {
		t.Fatalf("cell must re-author green: %+v", rep)
	}
	if !rep.Promoted {
		t.Fatalf("green + improved fitness must promote: %+v", rep)
	}
	if LoadLanguageVersion(le) != "flux/v2" {
		t.Fatal("promote must bump the live version")
	}
	// The promoted stack keeps the re-authored (now-correct) genome.
	if p, total, _ := orch.ScoreCell(ctx, urn); p != total || total == 0 {
		t.Fatalf("promoted cell must be green, got %d/%d", p, total)
	}
}

// Same setup, but the candidate's aggregate fitness did not improve → the epoch rolls
// back atomically: the live version stays, and the cell's ref is restored to the
// pre-epoch baseline (scoring 1/2 again) even though re-authoring itself succeeded.
func TestProposeLanguageChangeRollsBackViaReauthor(t *testing.T) {
	ctx := context.Background()
	le, repo := topoLedger(t)

	const urn = "urn:hdm:cell:reauth2"
	seed(t, repo, urn, `(module `+sharedImport+`
	  (func (export "run-tick") (param i32 i32) (result i32) i32.const 0))`) // baseline 1/2
	if err := SaveAcceptance(le, urn, &AcceptanceSuite{Tests: []AcceptanceTest{
		{Name: "one", Input: 1, Expected: 1},
		{Name: "zero", Input: 0, Expected: 0},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := RecordLineage(le, Lineage{Result: "g-old", URN: urn, Language: FluxLanguageVersion}); err != nil {
		t.Fatal(err)
	}

	identity := `(module ` + sharedImport + `
	  (func (export "run-tick") (param $p i32) (param $len i32) (result i32) local.get $p i32.load))`
	orch := NewOrchestrator(le, &fakeReasoner{responses: []string{identity}})

	rep, err := ProposeLanguageChange(le, "flux/v2", 0.9,
		orch.ReauthorCell(ctx, 3),
		func(outs []RebuildOutcome) float64 { return 0.5 }, // fitness dropped → reject
	)
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	if rep.Promoted {
		t.Fatalf("no fitness gain must not promote: %+v", rep)
	}
	if LoadLanguageVersion(le) != FluxLanguageVersion {
		t.Fatal("rollback must keep the old version live")
	}
	// Atomic rollback: the cell is back to its 1/2 baseline.
	if p, total, _ := orch.ScoreCell(ctx, urn); p != 1 || total != 2 {
		t.Fatalf("rollback must restore the baseline genome (1/2), got %d/%d", p, total)
	}
}

package evolution

import (
	"context"
	"strings"
	"testing"
)

// TestAcceptanceFrameDefersOptimizationUntilComplete verifies that while a cell
// still fails some acceptance checks, the loop does NOT anneal energy: a
// leaner-but-no-more-correct candidate (a tie in passing count) is held, not
// committed, so frames keep pushing to build the product rather than shrinking
// a half-built cell.
func TestAcceptanceFrameDefersOptimizationUntilComplete(t *testing.T) {
	ctx := context.Background()
	le, repo := topoLedger(t)

	const urn = "urn:hdm:cell:incomplete"
	// Baseline returns 0 always, via a redundant helper call (extra fuel). It
	// passes only the zero case => 1/2.
	seed(t, repo, urn, `(module `+sharedImport+`
	  (func $noop)
	  (func (export "run-tick") (param i32 i32) (result i32) call $noop i32.const 0))`)

	suite := &AcceptanceSuite{Tests: []AcceptanceTest{
		{Name: "one", Input: 1, Expected: 1},  // baseline fails this
		{Name: "zero", Input: 0, Expected: 0}, // baseline passes this
	}}
	if err := SaveAcceptance(le, urn, suite); err != nil {
		t.Fatalf("save acceptance: %v", err)
	}

	// Candidate: leaner (helper removed) but SAME behavior — still returns 0, so
	// still 1/2. A pure energy win with no correctness progress.
	leaner := `(module ` + sharedImport + `
	  (func (export "run-tick") (param i32 i32) (result i32) i32.const 0))`
	orch := NewOrchestrator(le, &fakeReasoner{responses: []string{leaner}})

	fr, err := orch.RunFrame(ctx, urn)
	if err != nil {
		t.Fatalf("run frame: %v", err)
	}
	if fr.AcceptBase != 1 || fr.AcceptTotal != 2 {
		t.Fatalf("scores: base=%d total=%d, want 1/2", fr.AcceptBase, fr.AcceptTotal)
	}
	if fr.Committed {
		t.Fatalf("must NOT optimize a half-built cell (1/2 passing), but committed: %s", fr.Reason)
	}
	if !strings.Contains(fr.Reason, "deferring optimization") {
		t.Fatalf("reason = %q, want a deferral message", fr.Reason)
	}
}

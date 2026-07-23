package evolution

import (
	"context"
	"testing"
)

// TestRunFrameBuildsRenderFrameCell verifies the loop can evolve a UI
// (render-frame) cell toward its draw-stream scenarios — not just run-tick
// cells. A renderer that draws nothing is built into one that draws a primitive,
// committed as correctness progress.
func TestRunFrameBuildsRenderFrameCell(t *testing.T) {
	le, repo := topoLedger(t)
	const urn = "urn:hdm:apps:g:renderer"

	// Baseline: a render-frame cell that emits an empty draw stream (0/1).
	seed(t, repo, urn, `(module `+sharedImport+`
	  (func (export "render-frame") (param i32 i32) (result i32) i32.const 0))`)
	if err := SaveAcceptance(le, urn, &AcceptanceSuite{Scenarios: []Scenario{{
		Name: "draws-something", Steps: 1, Entry: "render-frame",
		Expect: ScenarioExpect{Draw: &DrawExpect{MinRecords: 1}},
	}}}); err != nil {
		t.Fatalf("save acceptance: %v", err)
	}

	// The model returns a render-frame cell that writes one 24-byte rect record
	// and returns 24 — satisfying the "draws at least one primitive" scenario.
	cand := `(module ` + sharedImport + `
	  (func (export "render-frame") (param $b i32) (param $c i32) (result i32)
	    local.get $b i32.const 1 i32.store
	    local.get $b i32.const 4 i32.add i32.const 10 i32.store
	    local.get $b i32.const 8 i32.add i32.const 10 i32.store
	    local.get $b i32.const 12 i32.add i32.const 5 i32.store
	    local.get $b i32.const 16 i32.add i32.const 5 i32.store
	    local.get $b i32.const 20 i32.add i32.const 255 i32.store
	    i32.const 24))`

	orch := NewOrchestrator(le, &fakeReasoner{responses: []string{cand}})
	fr, err := orch.RunFrame(context.Background(), urn)
	if err != nil {
		t.Fatalf("frame: %v", err)
	}
	if !fr.Committed {
		t.Fatalf("render-frame build not committed: %s (base=%d cand=%d/%d)", fr.Reason, fr.AcceptBase, fr.AcceptCand, fr.AcceptTotal)
	}
	if !(fr.AcceptCand > fr.AcceptBase) || fr.AcceptTotal != 1 {
		t.Fatalf("expected correctness progress on the scenario: %d->%d/%d", fr.AcceptBase, fr.AcceptCand, fr.AcceptTotal)
	}
	// The committed cell now draws (its genotype is the render-frame candidate).
	d, _ := repo.Load(urn)
	g, _ := repo.Genotype(d)
	if !contains(g, "render-frame") {
		t.Fatalf("committed cell lost its render-frame entry: %s", g)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}

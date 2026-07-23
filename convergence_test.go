package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mdcfrancis/flow/compiler"
	"github.com/mdcfrancis/flow/evolution"
	"github.com/mdcfrancis/flow/manifest"
	"github.com/mdcfrancis/flow/status"
	"github.com/mdcfrancis/flow/storage"
)

// seqReasoner returns canned completions in order (for the orchestrator's model
// in stall/re-evaluate tests). nil model works when no model call is expected.
type seqReasoner struct {
	resp []string
	i    int
}

func (m *seqReasoner) InvokeReasoning(ctx context.Context, sys, user string) (string, error) {
	r := m.resp[m.i]
	if m.i < len(m.resp)-1 {
		m.i++
	}
	return r, nil
}

func newTestOrch(t *testing.T, model evolution.Reasoner) (*evolution.Orchestrator, *storage.LedgerEngine, *manifest.Repository) {
	t.Helper()
	le, err := storage.NewLedgerEngine(filepath.Join(t.TempDir(), "hdm.db"))
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	t.Cleanup(func() { le.Close() })
	return evolution.NewOrchestrator(le, model), le, manifest.NewRepository(le)
}

func TestApplicationFirstDeprioritizesSystemCells(t *testing.T) {
	// With application cells present, the system optimizer is excluded.
	mixed := []string{"urn:hdm:sys:optimizer", "urn:hdm:demo:wasteful", "urn:hdm:apps:x:core"}
	got := applicationFirst(mixed)
	if len(got) != 2 {
		t.Fatalf("applicationFirst = %v, want 2 non-system cells", got)
	}
	for _, u := range got {
		if u == "urn:hdm:sys:optimizer" {
			t.Fatalf("system cell should be excluded while app cells exist: %v", got)
		}
	}

	// When only system cells remain, they become eligible (still improvable).
	onlySys := []string{"urn:hdm:sys:optimizer"}
	if got := applicationFirst(onlySys); len(got) != 1 || got[0] != "urn:hdm:sys:optimizer" {
		t.Fatalf("applicationFirst(onlySys) = %v, want [optimizer]", got)
	}
}

func TestActiveCandidatesExcludesSettled(t *testing.T) {
	const root = "R1"
	all := []string{"a", "b", "c"}
	friction := map[string]evolution.FrictionState{
		"b": {Converged: true, Root: root},  // settled at the current root -> inactive
		"c": {Converged: true, Root: "OLD"}, // converged at a stale root -> re-evaluate
	}
	got := activeCandidates(all, friction, root)
	// a (never converged) and c (stale) are active; b is settled.
	if len(got) != 2 || got[0] != "a" || got[1] != "c" {
		t.Fatalf("activeCandidates = %v, want [a c] (b settled, c stale)", got)
	}
}

func TestNoteConvergenceMarksAndPersists(t *testing.T) {
	const root = "R1"
	friction := map[string]evolution.FrictionState{}
	persisted := map[string]evolution.FrictionState{}
	persist := func(u string, fs evolution.FrictionState) { friction[u] = fs; persisted[u] = fs }
	act := status.New()
	held := &evolution.FrameResult{TargetURN: "urn:x"}

	for i := 0; i < convergenceHolds-1; i++ {
		noteConvergence(held, friction, root, persist, act)
	}
	if friction["urn:x"].Converged {
		t.Fatalf("converged too early after %d holds", convergenceHolds-1)
	}
	// The Nth hold trips convergence at the current root, and it is persisted.
	if !noteConvergence(held, friction, root, persist, act) {
		t.Fatalf("expected justConverged on hold %d", convergenceHolds)
	}
	if !persisted["urn:x"].Settled(root) {
		t.Fatalf("converged state not persisted/settled: %+v", persisted["urn:x"])
	}
}

func TestNoteConvergenceResetsOnCommit(t *testing.T) {
	const root = "R1"
	friction := map[string]evolution.FrictionState{"urn:x": {Holds: 2}}
	persist := func(u string, fs evolution.FrictionState) { friction[u] = fs }
	committed := &evolution.FrameResult{TargetURN: "urn:x", Attempted: true, Committed: true}
	noteConvergence(committed, friction, root, persist, status.New())
	if friction["urn:x"].Holds != 0 || friction["urn:x"].Converged {
		t.Fatalf("commit should reset friction, got %+v", friction["urn:x"])
	}
}

func TestStalledCellIsParkedNotSettled(t *testing.T) {
	const root = "R1"
	friction := map[string]evolution.FrictionState{}
	persist := func(u string, fs evolution.FrictionState) { friction[u] = fs }
	// An incomplete cell (3/12) that makes no progress.
	held := &evolution.FrameResult{TargetURN: "urn:x", Attempted: true, AcceptBase: 3, AcceptTotal: 12}
	var justConverged bool
	for i := 0; i < convergenceHolds; i++ {
		justConverged = noteConvergence(held, friction, root, persist, status.New())
	}
	fs := friction["urn:x"]
	if !fs.Stalled(root) {
		t.Fatalf("incomplete cell should be STALLED, got %+v", fs)
	}
	if fs.Settled(root) {
		t.Fatalf("a stalled (incomplete) cell must NOT be settled: %+v", fs)
	}
	if !fs.Parked(root) {
		t.Fatalf("a stalled cell should be parked (out of active selection): %+v", fs)
	}
	if justConverged {
		t.Fatal("stalling is not 'converged' — must not trigger spec expansion")
	}
	// activeCandidates parks it; retryStalled re-opens it (budget remaining).
	if got := activeCandidates([]string{"urn:x"}, friction, root); len(got) != 0 {
		t.Fatalf("stalled cell should be parked out of active set, got %v", got)
	}
	orch, _, _ := newTestOrch(t, nil) // budget remaining => RecertifySuite not reached
	if n := retryStalled(context.Background(), orch, []string{"urn:x"}, friction, root, persist, status.New()); n != 1 {
		t.Fatalf("stalled cell with budget should be retried, got %d", n)
	}
	if friction["urn:x"].Converged || friction["urn:x"].Retries != 1 {
		t.Fatalf("retry should re-open and count: %+v", friction["urn:x"])
	}
}

func TestRetryStalledExhaustedNoSuiteStaysParked(t *testing.T) {
	const root = "R1"
	friction := map[string]evolution.FrictionState{
		"urn:x": {Converged: true, Incomplete: true, Retries: maxStallRetries, Root: root},
	}
	persist := func(u string, fs evolution.FrictionState) { friction[u] = fs }
	orch, _, _ := newTestOrch(t, nil) // no suite for urn:x => recertify is a no-op
	if n := retryStalled(context.Background(), orch, []string{"urn:x"}, friction, root, persist, status.New()); n != 0 {
		t.Fatalf("exhausted stall with no correctable tests must stay parked, got %d", n)
	}
}

func TestRetryStalledReevaluatesTests(t *testing.T) {
	const root = "R1"
	// Orchestrator whose auditor rejects one of two checks as unfaithful.
	orch, le, repo := newTestOrch(t, &seqReasoner{resp: []string{
		`{"verdicts":[{"name":"good","faithful":true},{"name":"bad","faithful":false,"reason":"not implied by the goal"}]}`,
	}})
	const urn = "urn:hdm:apps:x:core"
	wat := `(module (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 100))
	  (func (export "run-tick") (param i32 i32) (result i32) i32.const 0))`
	art, cErr := compiler.NewCompilerService().CompileGenotype(wat)
	if cErr != nil || !art.SyntaxPassed {
		t.Fatalf("compile: %v", cErr)
	}
	h, _, err := repo.PutCell(urn, wat, art.Bytecode, manifest.SemanticManifest{FunctionalIntent: "return the doubled input"}, 0)
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	_ = repo.SeedRef(urn, h)
	if err := evolution.SaveAcceptance(le, urn, &evolution.AcceptanceSuite{Tests: []evolution.AcceptanceTest{
		{Name: "good", Input: 2, Expected: 4},
		{Name: "bad", Input: 2, Expected: 99},
	}}); err != nil {
		t.Fatalf("save acceptance: %v", err)
	}

	friction := map[string]evolution.FrictionState{
		urn: {Converged: true, Incomplete: true, Retries: maxStallRetries, Root: root},
	}
	persist := func(u string, fs evolution.FrictionState) { friction[u] = fs }
	// Exhausted retries + a correctable suite => re-evaluated and re-opened.
	if n := retryStalled(context.Background(), orch, []string{urn}, friction, root, persist, status.New()); n != 1 {
		t.Fatalf("stalled cell with an unfaithful test should be re-evaluated and re-opened, got %d", n)
	}
	if friction[urn].Converged || friction[urn].Retries != 0 {
		t.Fatalf("re-evaluation should reset friction: %+v", friction[urn])
	}
	suite, _ := evolution.LoadAcceptance(le, urn)
	if suite == nil || len(suite.Tests) != 1 || suite.Tests[0].Name != "good" {
		t.Fatalf("unfaithful check should be dropped, suite=%+v", suite)
	}
}

func TestFrictionStaleRootReconfirms(t *testing.T) {
	// A cell converged at an old root re-confirms in one hold at the new root.
	friction := map[string]evolution.FrictionState{
		"urn:x": {Converged: true, Holds: convergenceHolds, Root: "OLD"},
	}
	persist := func(u string, fs evolution.FrictionState) { friction[u] = fs }
	held := &evolution.FrameResult{TargetURN: "urn:x"}
	noteConvergence(held, friction, "NEW", persist, status.New())
	if !friction["urn:x"].Settled("NEW") {
		t.Fatalf("stale-converged cell should re-confirm at the new root: %+v", friction["urn:x"])
	}
}

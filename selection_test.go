package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mdcfrancis/flow/compiler"
	"github.com/mdcfrancis/flow/evolution"
	"github.com/mdcfrancis/flow/manifest"
	"github.com/mdcfrancis/flow/storage"
)

func TestAppNamespace(t *testing.T) {
	cases := map[string]string{
		"urn:hdm:apps:space-invaders:renderer": "urn:hdm:apps:space-invaders",
		"urn:hdm:apps:x:core":                  "urn:hdm:apps:x",
		"urn:hdm:apps:x":                       "urn:hdm:apps:x",
		"urn:hdm:sys:optimizer":                "",
		"urn:hdm:demo:wasteful":                "",
	}
	for in, want := range cases {
		if got := appNamespace(in); got != want {
			t.Errorf("appNamespace(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestBuildFirstPrioritizesIncomplete verifies parallel co-evolution's enabler:
// among candidates, an incomplete cell is chosen directly for building; when all
// are complete, friction selection picks one; when nothing is buildable/probeable
// the target is "" (a fixpoint).
func TestChooseTargetPrefersIncomplete(t *testing.T) {
	le, err := storage.NewLedgerEngine(filepath.Join(t.TempDir(), "hdm.db"))
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	defer le.Close()
	repo := manifest.NewRepository(le)
	cs := compiler.NewCompilerService()
	shared := `(import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 100))`

	seed := func(urn, wat string) {
		art, cErr := cs.CompileGenotype(wat)
		if cErr != nil || !art.SyntaxPassed {
			t.Fatalf("compile %s: %v", urn, cErr)
		}
		h, _, pErr := repo.PutCell(urn, wat, art.Bytecode, manifest.SemanticManifest{}, 0)
		if pErr != nil {
			t.Fatalf("put %s: %v", urn, pErr)
		}
		if sErr := repo.SeedRef(urn, h); sErr != nil {
			t.Fatalf("ref %s: %v", urn, sErr)
		}
	}

	// "incomplete": returns 0, but its suite requires echoing the input => 0/1.
	const incomplete = "urn:hdm:apps:x:incomplete"
	seed(incomplete, `(module `+shared+`
	  (func (export "run-tick") (param i32 i32) (result i32) i32.const 0))`)
	if err := evolution.SaveAcceptance(le, incomplete, &evolution.AcceptanceSuite{
		Tests: []evolution.AcceptanceTest{{Name: "echo7", Input: 7, Expected: 7}},
	}); err != nil {
		t.Fatalf("save acceptance: %v", err)
	}

	// "nosuite": no acceptance checks at all (a fixed demo-style cell).
	const nosuite = "urn:hdm:apps:x:nosuite"
	seed(nosuite, `(module `+shared+`
	  (func (export "run-tick") (param i32 i32) (result i32) i32.const 1))`)

	// "trap": a run-tick cell that traps on the probe input 0 (like branchy).
	const trap = "urn:hdm:apps:x:trap"
	seed(trap, `(module `+shared+`
	  (func (export "run-tick") (param i32 i32) (result i32) unreachable))`)

	orch := evolution.NewOrchestrator(le, nil) // ScoreCell needs no model

	// The incomplete cell is chosen directly for building.
	if got := chooseTarget(context.Background(), orch, []string{nosuite, incomplete}); got != incomplete {
		t.Fatalf("chooseTarget = %q, want the incomplete cell", got)
	}
	// All complete: friction selection picks the probeable one.
	if got := chooseTarget(context.Background(), orch, []string{nosuite}); got != nosuite {
		t.Fatalf("chooseTarget(complete) = %q, want %q", got, nosuite)
	}
	// Nothing buildable and nothing probeable (only a trap-on-probe cell) => "".
	if got := chooseTarget(context.Background(), orch, []string{trap}); got != "" {
		t.Fatalf("chooseTarget(only-trap) = %q, want \"\" (fixpoint)", got)
	}
}

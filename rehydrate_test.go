package main

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/mdcfrancis/flow/appgen"
	"github.com/mdcfrancis/flow/compiler"
	"github.com/mdcfrancis/flow/evolution"
	"github.com/mdcfrancis/flow/manifest"
	"github.com/mdcfrancis/flow/status"
	"github.com/mdcfrancis/flow/storage"
)

// TestRehydrateReenrollsGrownCells simulates a restart: an app's envelope and
// its subsystem cells are persisted in the ledger; a fresh registry (as on boot)
// must be re-populated with the grown cells, and the UI cell surfaced.
func TestRehydrateReenrollsGrownCells(t *testing.T) {
	le, err := storage.NewLedgerEngine(filepath.Join(t.TempDir(), "hdm.db"))
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	defer le.Close()
	repo := manifest.NewRepository(le)
	cs := compiler.NewCompilerService()

	wat := `(module (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 100))
	  (func (export "run-tick") (param i32 i32) (result i32) i32.const 0))`
	seed := func(urn string) {
		art, cErr := cs.CompileGenotype(wat)
		if cErr != nil {
			t.Fatalf("compile: %v", cErr)
		}
		h, _, pErr := repo.PutCell(urn, wat, art.Bytecode, manifest.SemanticManifest{}, 0)
		if pErr != nil {
			t.Fatalf("put %s: %v", urn, pErr)
		}
		if sErr := repo.SeedRef(urn, h); sErr != nil {
			t.Fatalf("ref %s: %v", urn, sErr)
		}
	}
	// Two live subsystems; one envelope entry ("ghost") is intentionally NOT
	// seeded, to confirm only live descriptors are re-enrolled.
	seed("urn:hdm:apps:demo:core")
	seed("urn:hdm:apps:demo:view")

	env := appgen.AppEnvelope{
		ApplicationNamespace: "urn:hdm:apps:demo",
		Objective:            "a demo app",
		SubsystemRequirements: []appgen.Subsystem{
			{Identity: "urn:hdm:apps:demo:core", Semantics: "core logic"},
			{Identity: "urn:hdm:apps:demo:view", Semantics: "render the view to the canvas"},
			{Identity: "urn:hdm:apps:demo:ghost", Semantics: "never scaffolded"},
		},
	}
	raw, _ := json.Marshal(&env)
	h, err := le.WriteBlock(raw)
	if err != nil {
		t.Fatalf("write envelope: %v", err)
	}
	if err := le.UpdateRef("urn:hdm:apps:demo:envelope", h); err != nil {
		t.Fatalf("ref envelope: %v", err)
	}

	// Fresh registry, as on a cold boot (only the system seed).
	registry := evolution.NewCellRegistry("urn:hdm:sys:optimizer")
	ui := rehydrateApps(le, repo, registry, status.New())

	have := map[string]bool{}
	for _, u := range registry.List() {
		have[u] = true
	}
	if !have["urn:hdm:apps:demo:core"] || !have["urn:hdm:apps:demo:view"] {
		t.Fatalf("grown cells not re-enrolled: %v", registry.List())
	}
	if have["urn:hdm:apps:demo:ghost"] {
		t.Fatalf("ghost (no descriptor) should not be enrolled: %v", registry.List())
	}
	if ui != "urn:hdm:apps:demo:view" {
		t.Fatalf("rehydrated UI cell = %q, want the view cell", ui)
	}
}

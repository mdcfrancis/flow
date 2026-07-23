package appgen

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mdcfrancis/flow/compiler"
	"github.com/mdcfrancis/flow/evolution"
	"github.com/mdcfrancis/flow/manifest"
	"github.com/mdcfrancis/flow/storage"
)

func TestCriticizeCode(t *testing.T) {
	le, err := storage.NewLedgerEngine(filepath.Join(t.TempDir(), "hdm.db"))
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	defer le.Close()
	ns := "urn:hdm:apps:x"
	g := NewGrower(le, feedbackModel{})
	// Put one real cell so repo.Load/Genotype succeed (only mapped+loadable cells count).
	cs := compiler.NewCompilerService()
	art, cerr := cs.CompileGenotype(goodSkeleton)
	if cerr != nil || !art.SyntaxPassed {
		t.Fatalf("compile: %v", cerr)
	}
	h, _, err := g.repo.PutCell(ns+":a", goodSkeleton, art.Bytecode, manifest.SemanticManifest{FunctionalIntent: "does a"}, 0)
	if err != nil {
		t.Fatalf("putcell: %v", err)
	}
	if err := g.repo.SeedRef(ns+":a", h); err != nil {
		t.Fatalf("seedref: %v", err)
	}
	_ = evolution.SaveAppMap(le, ns, &evolution.AppMap{Namespace: ns, Components: []evolution.ComponentMap{
		{Identity: ns + ":a", Role: "does a"},
	}})
	// No criteria → nothing.
	if v, _ := g.CriticizeCode(context.Background(), ns, nil); v != nil {
		t.Error("no criteria should return nil")
	}
	// The model reports a violation for a REAL cell and a GHOST cell; only the real one survives.
	g.model = feedbackModel{out: `{"violations":[
	  {"cell":"urn:hdm:apps:x:a","criterion":"coordinate through shared state","reason":"uses a private buffer"},
	  {"cell":"urn:hdm:apps:x:ghost","criterion":"x","reason":"y"}
	]}`}
	vios, err := g.CriticizeCode(context.Background(), ns, []string{"coordinate through shared state"})
	if err != nil {
		t.Fatalf("criticize: %v", err)
	}
	if len(vios) != 1 || vios[0].Cell != ns+":a" {
		t.Fatalf("expected 1 grounded violation for :a, got %+v", vios)
	}
}

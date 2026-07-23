package engine

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mdcfrancis/flow/storage"
)

func newLedger(t *testing.T) *storage.LedgerEngine {
	t.Helper()
	le, err := storage.NewLedgerEngine(filepath.Join(t.TempDir(), "hdm.db"))
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	t.Cleanup(func() { le.Close() })
	return le
}

func TestProposeEvolutionCommitSucceeds(t *testing.T) {
	le := newLedger(t)
	mc := NewMVCCCoordinator(le)

	base, err := mc.InitManifest()
	if err != nil {
		t.Fatalf("init manifest: %v", err)
	}

	newRoot, err := mc.ProposeEvolutionCommit(context.Background(), base, "urn:hdm:sys:optimizer", "cafebabe")
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if newRoot == base {
		t.Fatal("manifest root did not advance")
	}

	// Target pointer advanced.
	got, err := le.GetRef("urn:hdm:sys:optimizer")
	if err != nil || got != "cafebabe" {
		t.Fatalf("target ref = %q, %v; want cafebabe", got, err)
	}
	// Manifest root advanced to the returned value.
	root, _ := le.GetRef(ManifestRootURN)
	if root != newRoot {
		t.Fatalf("manifest root = %q; want %q", root, newRoot)
	}
}

func TestProposeEvolutionCommitDetectsConflict(t *testing.T) {
	le := newLedger(t)
	mc := NewMVCCCoordinator(le)

	base, err := mc.InitManifest()
	if err != nil {
		t.Fatalf("init: %v", err)
	}

	// A concurrent evolution advances the root out from under us.
	if _, err := mc.ProposeEvolutionCommit(context.Background(), base, "urn:hdm:cell:a", "1111"); err != nil {
		t.Fatalf("first commit: %v", err)
	}

	// Our commit still references the now-stale baseRoot => must abort.
	_, err = mc.ProposeEvolutionCommit(context.Background(), base, "urn:hdm:cell:b", "2222")
	if err == nil {
		t.Fatal("expected MVCC concurrency conflict, got nil")
	}

	// The aborted commit must not have contaminated cell:b.
	if _, err := le.GetRef("urn:hdm:cell:b"); err == nil {
		t.Fatal("aborted commit leaked a target pointer for cell:b")
	}
}

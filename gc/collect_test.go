package gc

import (
	"path/filepath"
	"testing"

	"github.com/mdcfrancis/flow/evolution"
	"github.com/mdcfrancis/flow/manifest"
	"github.com/mdcfrancis/flow/storage"
	"github.com/mdcfrancis/flow/tapes"
)

func TestCollectSweepsOrphansKeepsReachable(t *testing.T) {
	le, err := storage.NewLedgerEngine(filepath.Join(t.TempDir(), "hdm.db"))
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	defer le.Close()

	repo := manifest.NewRepository(le)
	// A live cell: descriptor + genotype + phenotype blocks are all reachable.
	descHash, desc, err := repo.PutCell("urn:hdm:cell:c", "(module)", []byte{1, 2, 3}, manifest.SemanticManifest{}, 0)
	if err != nil {
		t.Fatalf("put cell: %v", err)
	}
	repo.SeedRef("urn:hdm:cell:c", descHash)

	// Reachable tapes via the index.
	store := evolution.NewTapeStore(le)
	if err := store.Append("urn:hdm:cell:c", []*tapes.TransactionFrame{
		{MonadicTimestampNS: 1, InputPayload: []byte{7}},
	}); err != nil {
		t.Fatalf("append tape: %v", err)
	}

	// An orphan block referenced by nothing.
	orphan, err := le.WriteBlock([]byte("garbage-not-referenced-by-any-ref"))
	if err != nil {
		t.Fatalf("write orphan: %v", err)
	}

	scanned, deleted, err := Collect(le)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if deleted < 1 {
		t.Fatalf("expected the orphan swept, deleted=%d scanned=%d", deleted, scanned)
	}

	// Orphan gone.
	if _, err := le.ReadBlock(orphan); err == nil {
		t.Fatal("orphan block survived GC")
	}
	// Reachable blocks retained.
	for _, h := range []string{descHash, desc.GenotypeHash, desc.PhenotypeHash} {
		if _, err := le.ReadBlock(h); err != nil {
			t.Fatalf("reachable block %s was swept: %v", h, err)
		}
	}
	// A second pass is a no-op (nothing left to collect).
	if _, deleted2, _ := Collect(le); deleted2 != 0 {
		t.Fatalf("second GC pass deleted %d, want 0", deleted2)
	}
}

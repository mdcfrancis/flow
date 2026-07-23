package codependency

import (
	"path/filepath"
	"testing"

	"github.com/mdcfrancis/flow/storage"
)

func TestGravityAccumulationAndBatch(t *testing.T) {
	le, err := storage.NewLedgerEngine(filepath.Join(t.TempDir(), "hdm.db"))
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	defer le.Close()
	tr := NewTracker(le)

	const x, yHot, yCold = "urn:hdm:cell:x", "urn:hdm:cell:hot", "urn:hdm:cell:cold"

	// Four isolation passes on x.
	for i := 0; i < 4; i++ {
		if err := tr.RecordIsolationAttempt(x); err != nil {
			t.Fatalf("attempt: %v", err)
		}
	}
	// x regressed the "hot" neighbor 3 of those times, "cold" once.
	for i := 0; i < 3; i++ {
		if _, err := tr.RecordJointFailure(x, yHot); err != nil {
			t.Fatalf("joint: %v", err)
		}
	}
	if _, err := tr.RecordJointFailure(x, yCold); err != nil {
		t.Fatalf("joint: %v", err)
	}

	m, err := tr.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if g := m.Gravity(x, yHot); g != 0.75 { // 3/4
		t.Fatalf("Gravity(x,hot)=%v, want 0.75", g)
	}
	if g := m.Gravity(x, yCold); g != 0.25 { // 1/4
		t.Fatalf("Gravity(x,cold)=%v, want 0.25", g)
	}

	// At threshold 0.5 only the hot neighbor batches with x.
	batch, err := tr.Batch(x, 0.5)
	if err != nil {
		t.Fatalf("batch: %v", err)
	}
	if len(batch) != 2 || batch[0] != x {
		t.Fatalf("batch = %v, want [x, hot]", batch)
	}
	found := false
	for _, u := range batch {
		if u == yHot {
			found = true
		}
		if u == yCold {
			t.Fatalf("cold neighbor should not batch at threshold 0.5: %v", batch)
		}
	}
	if !found {
		t.Fatalf("hot neighbor missing from batch: %v", batch)
	}
}

func TestGravityEmptyMatrix(t *testing.T) {
	le, err := storage.NewLedgerEngine(filepath.Join(t.TempDir(), "hdm.db"))
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	defer le.Close()
	tr := NewTracker(le)

	batch, err := tr.Batch("urn:hdm:cell:lonely", 0.5)
	if err != nil {
		t.Fatalf("batch on empty: %v", err)
	}
	if len(batch) != 1 || batch[0] != "urn:hdm:cell:lonely" {
		t.Fatalf("empty-matrix batch = %v, want [self]", batch)
	}
}

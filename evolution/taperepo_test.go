package evolution

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mdcfrancis/flow/compiler"
	"github.com/mdcfrancis/flow/storage"
	"github.com/mdcfrancis/flow/tapes"
)

func newTapeLedger(t *testing.T) *storage.LedgerEngine {
	t.Helper()
	le, err := storage.NewLedgerEngine(filepath.Join(t.TempDir(), "hdm.db"))
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	t.Cleanup(func() { le.Close() })
	return le
}

func TestTapeStoreRoundTrip(t *testing.T) {
	store := NewTapeStore(newTapeLedger(t))
	const urn = "urn:hdm:cell:x"

	mk := func(b byte) *tapes.TransactionFrame {
		return &tapes.TransactionFrame{MonadicTimestampNS: uint64(b), InputPayload: []byte{b}}
	}
	if err := store.Append(urn, []*tapes.TransactionFrame{mk(1), mk(2)}); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := store.Append(urn, []*tapes.TransactionFrame{mk(3)}); err != nil {
		t.Fatalf("append 2: %v", err)
	}
	n, _ := store.Count(urn)
	if n != 3 {
		t.Fatalf("count = %d, want 3", n)
	}
	frames, err := store.Load(urn)
	if err != nil || len(frames) != 3 {
		t.Fatalf("load: %d frames, %v", len(frames), err)
	}
	if frames[0].InputPayload[0] != 1 || frames[2].InputPayload[0] != 3 {
		t.Fatalf("order/content wrong: %v", frames)
	}

	// Replace shrinks the index.
	if err := store.Replace(urn, []*tapes.TransactionFrame{mk(9)}); err != nil {
		t.Fatalf("replace: %v", err)
	}
	n, _ = store.Count(urn)
	if n != 1 {
		t.Fatalf("after replace count = %d, want 1", n)
	}
}

func TestJanitorPrunesDuplicates(t *testing.T) {
	ctx := context.Background()
	le := newTapeLedger(t)
	store := NewTapeStore(le)
	const urn = "urn:hdm:cell:branchy"

	art, err := compiler.NewCompilerService().CompileGenotype(branchyDiscoveryCell)
	if err != nil || !art.SyntaxPassed {
		t.Fatalf("compile: %v", err)
	}

	// Seed the repository with many redundant tapes: the same handful of input
	// bytes repeated, so most are duplicates by execution path + state delta.
	var frames []*tapes.TransactionFrame
	for i := 0; i < 40; i++ {
		b := byte((i % 4) + 40) // only 4 distinct low-branch inputs, all repeated
		frames = append(frames, &tapes.TransactionFrame{
			MonadicTimestampNS: uint64(i), InputPayload: []byte{b},
		})
	}
	if err := store.Append(urn, frames); err != nil {
		t.Fatalf("append: %v", err)
	}

	janitor := NewJanitor(store, "run-tick", DefaultPayloadOffset, DefaultStateWindow, nil)
	before, after, err := janitor.Compact(ctx, urn, art.Bytecode)
	if err != nil {
		t.Fatalf("compact: %v", err)
	}
	if before != 40 {
		t.Fatalf("before = %d, want 40", before)
	}
	if after >= before {
		t.Fatalf("no compaction: %d -> %d", before, after)
	}
	// Persisted count must match what the janitor reported.
	n, _ := store.Count(urn)
	if n != after {
		t.Fatalf("stored count %d != reported after %d", n, after)
	}
	t.Logf("janitor pruned %d -> %d", before, after)
}

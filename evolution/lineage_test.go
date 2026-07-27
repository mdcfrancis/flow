package evolution

import (
	"path/filepath"
	"testing"

	"github.com/mdcfrancis/flow/storage"
)

func newLedger(t *testing.T) *storage.LedgerEngine {
	t.Helper()
	le, err := storage.NewLedgerEngine(filepath.Join(t.TempDir(), "lineage.db"))
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	return le
}

func TestLineageRecordAndProvenance(t *testing.T) {
	le := newLedger(t)
	if _, ok := LineageOf(le, "abc"); ok {
		t.Fatal("empty store should have no lineage")
	}
	l := Lineage{
		Result: "abc", URN: "urn:hdm:app:demo:physics",
		Language: "L1", Grammar: "G1", Prompt: "P1", Model: "qwen",
		Examples: []string{"ex1", "ex2"}, Scenarios: "S1", Contract: "C1",
	}
	if err := RecordLineage(le, l); err != nil {
		t.Fatalf("record: %v", err)
	}
	got, ok := LineageOf(le, "abc")
	if !ok {
		t.Fatal("provenance lookup missed a recorded result")
	}
	if got.Language != "L1" || got.URN != "urn:hdm:app:demo:physics" || len(got.Examples) != 2 {
		t.Fatalf("round-trip lost data: %+v", got)
	}
}

func TestRecordLineageDropsResultless(t *testing.T) {
	le := newLedger(t)
	if err := RecordLineage(le, Lineage{Language: "L1"}); err != nil {
		t.Fatalf("record: %v", err)
	}
	if len(LoadLineages(le)) != 0 {
		t.Fatal("a lineage with no Result must not be stored")
	}
}

func TestRecordLineageReplacesByResult(t *testing.T) {
	le := newLedger(t)
	_ = RecordLineage(le, Lineage{Result: "r", Language: "L1"})
	_ = RecordLineage(le, Lineage{Result: "r", Language: "L2"})
	ls := LoadLineages(le)
	if len(ls) != 1 {
		t.Fatalf("same Result must replace, not append: got %d", len(ls))
	}
	if ls[0].Language != "L2" {
		t.Fatalf("replacement lost: %+v", ls[0])
	}
}

func TestInputsHashStableAndSetOrderIndependent(t *testing.T) {
	a := Lineage{Result: "r1", Language: "L1", Examples: []string{"ex1", "ex2"}, Leaves: []string{"a", "b"}}
	b := Lineage{Result: "r2", Language: "L1", Examples: []string{"ex2", "ex1"}, Leaves: []string{"b", "a"}}
	if a.InputsHash() != b.InputsHash() {
		t.Fatal("InputsHash must be independent of example/leaf order (and of Result)")
	}
	c := Lineage{Result: "r3", Language: "L2", Examples: []string{"ex1", "ex2"}}
	if a.InputsHash() == c.InputsHash() {
		t.Fatal("a changed input (language) must change InputsHash")
	}
}

// The rebuild memo: same inputs → cache hit returning the stored result; a moved
// input → miss. docs/lineage.md §2, §5.
func TestFindLineageByInputsIsTheMemo(t *testing.T) {
	le := newLedger(t)
	authored := Lineage{Result: "geno-1", Language: "L1", Grammar: "G1", Examples: []string{"ex1"}}
	_ = RecordLineage(le, authored)

	hit, ok := FindLineageByInputs(le, authored.InputsHash())
	if !ok || hit.Result != "geno-1" {
		t.Fatalf("unchanged inputs must be a cache hit returning the stored genotype; got %+v ok=%v", hit, ok)
	}

	moved := Lineage{Language: "L2", Grammar: "G1", Examples: []string{"ex1"}} // language changed
	if _, ok := FindLineageByInputs(le, moved.InputsHash()); ok {
		t.Fatal("a moved input hash must miss the memo (forces re-authoring)")
	}
}

func TestFindLineageByInputsPrefersLatest(t *testing.T) {
	le := newLedger(t)
	// Two results with identical inputs (a nondeterministic re-authoring): the later
	// record supersedes the earlier for the memo.
	_ = RecordLineage(le, Lineage{Result: "old", Language: "L1"})
	_ = RecordLineage(le, Lineage{Result: "new", Language: "L1"})
	hit, ok := FindLineageByInputs(le, Lineage{Language: "L1"}.InputsHash())
	if !ok || hit.Result != "new" {
		t.Fatalf("latest record must win the memo; got %+v ok=%v", hit, ok)
	}
}

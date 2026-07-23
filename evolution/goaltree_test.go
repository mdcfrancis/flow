package evolution

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/mdcfrancis/flow/storage"
)

func TestGoalTreeRoundTrip(t *testing.T) {
	le, err := storage.NewLedgerEngine(filepath.Join(t.TempDir(), "hdm.db"))
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	defer le.Close()

	if LoadGoalTree(le, "urn:hdm:apps:x") != nil {
		t.Fatal("missing goal tree should be nil")
	}
	tr := NewGoalTree("urn:hdm:apps:x", "move a dot")
	tr.SeedSubsystem("urn:hdm:apps:x:input", "move the player")
	tr.SeedSubsystem("urn:hdm:apps:x:view", "draw the player")
	if err := SaveGoalTree(le, "urn:hdm:apps:x", tr); err != nil {
		t.Fatalf("save: %v", err)
	}
	got := LoadGoalTree(le, "urn:hdm:apps:x")
	if got == nil || len(got.Nodes) != 3 { // root + 2 subsystems
		t.Fatalf("roundtrip nodes = %d, want 3", len(got.Nodes))
	}
	if !reflect.DeepEqual(got.DFSCells(),
		[]string{"urn:hdm:apps:x:input", "urn:hdm:apps:x:view"}) {
		t.Errorf("DFS after roundtrip = %v", got.DFSCells())
	}
}

func TestGoalTreeSeedIsIdempotent(t *testing.T) {
	tr := NewGoalTree("urn:hdm:apps:x", "obj")
	tr.SeedSubsystem("urn:hdm:apps:x:a", "a")
	tr.SeedSubsystem("urn:hdm:apps:x:a", "a again")
	if got := tr.DFSCells(); len(got) != 1 {
		t.Fatalf("seeding twice should be a no-op, got %v", got)
	}
}

// The core Phase-1 property: a fractured parent's children are walked immediately
// after it and BEFORE the next top-level sibling — depth-first, not breadth-first.
func TestGoalTreeFractureOrdersChildrenBeforeSiblings(t *testing.T) {
	tr := NewGoalTree("urn:hdm:apps:x", "obj")
	tr.SeedSubsystem("urn:hdm:apps:x:physics", "physics")
	tr.SeedSubsystem("urn:hdm:apps:x:view", "view")

	tr.RecordFracture("urn:hdm:apps:x:physics", []GoalSpec{
		{Cell: "urn:hdm:apps:x:physics-move", Objective: "move"},
		{Cell: "urn:hdm:apps:x:physics-collide", Objective: "collide"},
	}, "too big to synthesize whole")

	// Pre-order: physics (branch), its two children, THEN view.
	want := []string{
		"urn:hdm:apps:x:physics",
		"urn:hdm:apps:x:physics-move",
		"urn:hdm:apps:x:physics-collide",
		"urn:hdm:apps:x:view",
	}
	if got := tr.DFSCells(); !reflect.DeepEqual(got, want) {
		t.Fatalf("DFS = %v\nwant %v", got, want)
	}

	// The decompose reason is retained on the parent for backtracking (Phase 2).
	parent := tr.Nodes["urn:hdm:apps:x:physics"]
	if parent.Kind != "branch" || len(parent.Attempts) != 1 ||
		parent.Attempts[0].Reason != "too big to synthesize whole" {
		t.Errorf("parent attempt not recorded: %+v", parent)
	}
}

// Order() must impose DFS order on an arbitrary candidate slice (e.g. registry
// insertion order, where fracture children land last) and never drop a candidate.
func TestGoalTreeOrderReordersCandidates(t *testing.T) {
	tr := NewGoalTree("urn:hdm:apps:x", "obj")
	tr.SeedSubsystem("urn:hdm:apps:x:physics", "physics")
	tr.SeedSubsystem("urn:hdm:apps:x:view", "view")
	tr.RecordFracture("urn:hdm:apps:x:physics", []GoalSpec{
		{Cell: "urn:hdm:apps:x:physics-move", Objective: "move"},
	}, "stall")

	// Registry order: children appended last, plus an untracked cell.
	candidates := []string{
		"urn:hdm:apps:x:view",
		"urn:hdm:apps:x:physics",
		"urn:hdm:apps:x:physics-move",
		"urn:hdm:apps:x:untracked",
	}
	got := tr.Order(candidates)
	want := []string{
		"urn:hdm:apps:x:physics",      // parent first
		"urn:hdm:apps:x:physics-move", // its child before the sibling
		"urn:hdm:apps:x:view",         // sibling
		"urn:hdm:apps:x:untracked",    // unknown → appended, preserved
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Order = %v\nwant %v", got, want)
	}
}

func TestGoalTreeArchivedSubtreeSkipped(t *testing.T) {
	tr := NewGoalTree("urn:hdm:apps:x", "obj")
	tr.SeedSubsystem("urn:hdm:apps:x:a", "a")
	tr.SeedSubsystem("urn:hdm:apps:x:b", "b")
	tr.Nodes["urn:hdm:apps:x:a"].Archived = true
	if got := tr.DFSCells(); !reflect.DeepEqual(got, []string{"urn:hdm:apps:x:b"}) {
		t.Fatalf("archived node should be skipped, got %v", got)
	}
}

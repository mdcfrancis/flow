package evolution

import "testing"

func TestSurfaceLedgerRoundTrip(t *testing.T) {
	le := newLedger(t)
	if s := LoadSurface(le); s != "forth" {
		t.Fatalf("default surface = %q, want forth (the operational standard)", s)
	}
	if err := SaveSurface(le, "sexpr"); err != nil {
		t.Fatal(err)
	}
	if s := LoadSurface(le); s != "sexpr" {
		t.Fatalf("after save, surface = %q, want sexpr", s)
	}
}

// Accepted surface epoch: every sexpr-authored cell re-authors green in forth → the
// default surface flips and the new genomes are kept.
func TestPromoteSurfacePromotes(t *testing.T) {
	le := newLedger(t)
	setActiveSurface("sexpr")
	t.Cleanup(func() { setActiveSurface("sexpr") })

	// Distinct scenarios so the two cells have distinct InputsHashes (as real cells do)
	// and neither is served from the other's memo.
	_ = RecordLineage(le, Lineage{Result: "p1", URN: "urn:app:physics", Language: "flux/v1", Surface: "sexpr", Scenarios: "bounce"})
	_ = RecordLineage(le, Lineage{Result: "r1", URN: "urn:app:render", Language: "flux/v1", Surface: "sexpr", Scenarios: "draw"})
	_ = le.UpdateRef("urn:app:physics", "desc-old-p")
	_ = le.UpdateRef("urn:app:render", "desc-old-r")

	reauthor := func(stale, migrated Lineage) (string, bool, float64, error) {
		if migrated.Surface != "forth" {
			t.Fatalf("cell must be re-authored in the NEW surface, got %q", migrated.Surface)
		}
		if currentSurface() != "forth" {
			t.Fatal("the surface must be active during the rebuild")
		}
		_ = le.UpdateRef(stale.URN, "desc-new") // the real path commits a new genome
		return "g-" + stale.URN, true, 1.0, nil
	}
	rep, err := PromoteSurface(le, "forth", reauthor)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.AllGreen || !rep.Promoted || rep.Rebuilt != 2 {
		t.Fatalf("all-green rebuild must promote both cells: %+v", rep)
	}
	if LoadSurface(le) != "forth" || currentSurface() != "forth" {
		t.Fatal("promote must make forth the live + persisted default")
	}
}

// Rejected surface epoch: a cell fails to re-author green → roll back (surface stays,
// refs restored).
func TestPromoteSurfaceRollsBack(t *testing.T) {
	le := newLedger(t)
	_ = SaveSurface(le, "sexpr") // pin the ledger so rollback is observable vs the forth default
	setActiveSurface("sexpr")
	t.Cleanup(func() { setActiveSurface("sexpr") })

	_ = RecordLineage(le, Lineage{Result: "p1", URN: "urn:app:physics", Language: "flux/v1", Surface: "sexpr"})
	_ = le.UpdateRef("urn:app:physics", "desc-old")

	reauthor := func(stale, _ Lineage) (string, bool, float64, error) {
		_ = le.UpdateRef(stale.URN, "desc-new")
		return "g", false, 0, nil // NOT green in forth
	}
	rep, err := PromoteSurface(le, "forth", reauthor)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Promoted {
		t.Fatalf("a non-green rebuild must not promote: %+v", rep)
	}
	if LoadSurface(le) == "forth" || currentSurface() != "sexpr" {
		t.Fatal("rollback must keep sexpr the default")
	}
	if h, _ := le.GetRef("urn:app:physics"); h != "desc-old" {
		t.Fatalf("rollback must restore the prior ref, got %q", h)
	}
}

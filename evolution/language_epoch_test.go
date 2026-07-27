package evolution

import "testing"

func TestLanguageVersionDefaultsAndPersists(t *testing.T) {
	le := newLedger(t)
	if v := LoadLanguageVersion(le); v != FluxLanguageVersion {
		t.Fatalf("default language version = %q, want %q", v, FluxLanguageVersion)
	}
	if err := SaveLanguageVersion(le, "flux/v2"); err != nil {
		t.Fatal(err)
	}
	if v := LoadLanguageVersion(le); v != "flux/v2" {
		t.Fatalf("after promote, version = %q, want flux/v2", v)
	}
}

// Accepted ΔL: every re-derived cell green and fitness improved → promote (version
// bumped, the re-derived genomes kept).
func TestProposeLanguageChangePromotes(t *testing.T) {
	le := newLedger(t)
	_ = RecordLineage(le, Lineage{Result: "p1", URN: "urn:app:physics", Language: FluxLanguageVersion})
	_ = le.UpdateRef("urn:app:physics", "desc-old")

	reauthor := func(_, _ Lineage) (string, bool, float64, error) {
		_ = le.UpdateRef("urn:app:physics", "desc-new") // the real path commits a new genome
		return "p2", true, 0, nil
	}
	rep, err := ProposeLanguageChange(le, "flux/v2", 0.5, reauthor, func([]RebuildOutcome) float64 { return 0.9 })
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Promoted {
		t.Fatalf("green + improved fitness must promote: %+v", rep)
	}
	if LoadLanguageVersion(le) != "flux/v2" {
		t.Fatal("promote must bump the live language version")
	}
	if h, _ := le.GetRef("urn:app:physics"); h != "desc-new" {
		t.Fatalf("promote must keep the re-derived genome, ref=%q", h)
	}
	if rep.Rebuilt != 1 {
		t.Fatalf("expected 1 rebuilt cell, got %d", rep.Rebuilt)
	}
}

// Rejected ΔL: fitness did not improve → roll back (version unchanged, refs
// restored) even though the cells re-derived green.
func TestProposeLanguageChangeRollsBackOnNoFitnessGain(t *testing.T) {
	le := newLedger(t)
	_ = RecordLineage(le, Lineage{Result: "p1", URN: "urn:app:physics", Language: FluxLanguageVersion})
	_ = le.UpdateRef("urn:app:physics", "desc-old")

	reauthor := func(_, _ Lineage) (string, bool, float64, error) {
		_ = le.UpdateRef("urn:app:physics", "desc-new")
		return "p2", true, 0, nil // green...
	}
	rep, err := ProposeLanguageChange(le, "flux/v2", 0.9, reauthor, func([]RebuildOutcome) float64 { return 0.5 }) // ...but fitness dropped
	if err != nil {
		t.Fatal(err)
	}
	if rep.Promoted {
		t.Fatalf("no fitness gain must NOT promote: %+v", rep)
	}
	if LoadLanguageVersion(le) != FluxLanguageVersion {
		t.Fatal("rollback must leave the old language version live")
	}
	if h, _ := le.GetRef("urn:app:physics"); h != "desc-old" {
		t.Fatalf("rollback must restore the prior ref, got %q", h)
	}
}

// Rejected ΔL: a cell failed to re-derive green → roll back regardless of fitness.
func TestProposeLanguageChangeRollsBackOnRegression(t *testing.T) {
	le := newLedger(t)
	_ = RecordLineage(le, Lineage{Result: "p1", URN: "urn:app:physics", Language: FluxLanguageVersion})
	_ = le.UpdateRef("urn:app:physics", "desc-old")

	reauthor := func(_, _ Lineage) (string, bool, float64, error) {
		_ = le.UpdateRef("urn:app:physics", "desc-new")
		return "p2", false, 0, nil // NOT green
	}
	rep, err := ProposeLanguageChange(le, "flux/v2", 0.0, reauthor, func([]RebuildOutcome) float64 { return 1.0 })
	if err != nil {
		t.Fatal(err)
	}
	if rep.AllGreen {
		t.Fatal("a non-green re-derivation must make AllGreen false")
	}
	if rep.Promoted {
		t.Fatalf("a regression must never promote even with higher fitness: %+v", rep)
	}
	if h, _ := le.GetRef("urn:app:physics"); h != "desc-old" {
		t.Fatalf("rollback must restore the prior ref, got %q", h)
	}
}

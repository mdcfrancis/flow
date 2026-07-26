package evolution

import "testing"

func TestPlanRebuildPartitionsByLanguage(t *testing.T) {
	le := newLedger(t)
	_ = RecordLineage(le, Lineage{Result: "p1", URN: "urn:app:physics", Language: "flux/v1", Examples: []string{"ex"}})
	_ = RecordLineage(le, Lineage{Result: "r1", URN: "urn:app:render", Language: "flux/v1"})
	_ = RecordLineage(le, Lineage{Result: "w1", URN: "urn:app:wat", Language: langWAT})
	_ = RecordLineage(le, Lineage{Result: "n1", URN: "urn:app:new", Language: "flux/v2"})

	plan := PlanRebuild(le, CurrentInputs{Language: "flux/v2"})
	if len(plan.Stale) != 2 {
		t.Fatalf("only the two flux/v1 cells are stale under flux/v2; got %d: %+v", len(plan.Stale), plan.Stale)
	}
	if len(plan.Hits) != 2 {
		t.Fatalf("the WAT cell and the already-migrated cell are hits; got %d: %+v", len(plan.Hits), plan.Hits)
	}
	for _, s := range plan.Stale {
		if s.Language != "flux/v1" {
			t.Fatalf("a non-flux/v1 cell was marked stale: %+v", s)
		}
	}
}

// Execute reuses hits without re-deriving them (the base case that breaks the
// self-hosting cycle), re-derives stale cells, and records their migrated lineage.
func TestExecuteReusesHitsAndReauthorsStale(t *testing.T) {
	le := newLedger(t)
	_ = RecordLineage(le, Lineage{Result: "p1", URN: "urn:app:physics", Language: "flux/v1", Examples: []string{"ex"}})
	_ = RecordLineage(le, Lineage{Result: "w1", URN: "urn:app:wat", Language: langWAT})

	cur := CurrentInputs{Language: "flux/v2"}
	plan := PlanRebuild(le, cur)

	var reauthored []string
	reauthor := func(stale, migrated Lineage) (string, bool, float64, error) {
		reauthored = append(reauthored, stale.URN)
		if migrated.Language != "flux/v2" {
			t.Fatalf("stale cell must be re-derived under the NEW language, got %q", migrated.Language)
		}
		return "p2", true, 1.0, nil // new genotype, green
	}
	outcomes, err := plan.Execute(le, cur, reauthor)
	if err != nil {
		t.Fatal(err)
	}
	if len(reauthored) != 1 || reauthored[0] != "urn:app:physics" {
		t.Fatalf("exactly the stale Flux cell is re-derived; a hit must never be (cycle break). got %v", reauthored)
	}
	if !AllGreen(outcomes) {
		t.Fatal("all re-derived cells green → AllGreen must hold")
	}
	// The migrated physics lineage was recorded under the new language.
	got, ok := LineageOf(le, "p2")
	if !ok || got.Language != "flux/v2" || got.URN != "urn:app:physics" {
		t.Fatalf("migrated lineage not recorded: %+v ok=%v", got, ok)
	}
}

// Early cutoff: if the migrated inputs already produced a genotype (a prior epoch),
// Execute reuses it instead of re-deriving — docs/lineage.md §5.
func TestExecuteEarlyCutoffOnMemoHit(t *testing.T) {
	le := newLedger(t)
	stale := Lineage{Result: "p1", URN: "urn:app:physics", Language: "flux/v1", Examples: []string{"ex"}}
	_ = RecordLineage(le, stale)
	// Pre-record the migrated authoring (as if a prior epoch already did it).
	cur := CurrentInputs{Language: "flux/v2"}
	migrated := cur.migrated(stale)
	migrated.Result = "p_cached"
	_ = RecordLineage(le, migrated)

	plan := PlanRebuild(le, cur)
	called := false
	reauthor := func(_, _ Lineage) (string, bool, float64, error) {
		called = true
		return "should-not-run", false, 0, nil
	}
	outcomes, err := plan.Execute(le, cur, reauthor)
	if err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("an exact-inputs memo hit must skip re-derivation (early cutoff)")
	}
	// physics should be reused from the cache.
	var found bool
	for _, o := range outcomes {
		if o.URN == "urn:app:physics" {
			found = true
			if o.Result != "p_cached" || !o.Reused {
				t.Fatalf("physics should be served from the memo: %+v", o)
			}
		}
	}
	if !found {
		t.Fatal("physics outcome missing")
	}
}

// A stale cell that comes back non-green fails AllGreen — the behavioral half of the
// epoch acceptance gate.
func TestAllGreenFailsOnRegression(t *testing.T) {
	le := newLedger(t)
	_ = RecordLineage(le, Lineage{Result: "p1", URN: "urn:app:physics", Language: "flux/v1"})
	cur := CurrentInputs{Language: "flux/v2"}
	plan := PlanRebuild(le, cur)
	outcomes, err := plan.Execute(le, cur, func(_, _ Lineage) (string, bool, float64, error) {
		return "p2", false, 0, nil // re-derived but NOT green
	})
	if err != nil {
		t.Fatal(err)
	}
	if AllGreen(outcomes) {
		t.Fatal("a re-derived cell that is not green must fail AllGreen")
	}
}

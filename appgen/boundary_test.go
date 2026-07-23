package appgen

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mdcfrancis/flow/evolution"
	"github.com/mdcfrancis/flow/storage"
)

type boundaryModel struct{ out string }

func (m boundaryModel) InvokeReasoning(ctx context.Context, sys, user string) (string, error) {
	return m.out, nil
}

func hasStr(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func TestBoundaryFriction(t *testing.T) {
	c := &evolution.ComponentMap{
		DeclaredReads:  []string{"a", "unused_r"},
		DeclaredWrites: []string{"b"},
		Reads:          []string{"a", "c"}, // c: reads but didn't declare
		Writes:         []string{"b", "d"}, // d: writes but didn't declare
	}
	f := boundaryFriction(c)
	if !hasStr(f.UndeclaredReads, "c") {
		t.Errorf("expected undeclared read c: %v", f.UndeclaredReads)
	}
	if !hasStr(f.UndeclaredWrites, "d") {
		t.Errorf("expected undeclared write d: %v", f.UndeclaredWrites)
	}
	if !hasStr(f.UnusedReads, "unused_r") {
		t.Errorf("expected unused read unused_r: %v", f.UnusedReads)
	}
	if len(f.UnusedWrites) != 0 {
		t.Errorf("expected no unused writes: %v", f.UnusedWrites)
	}
	if f.None() {
		t.Fatal("expected friction, got none")
	}
}

func TestEvolveBoundary(t *testing.T) {
	le, err := storage.NewLedgerEngine(filepath.Join(t.TempDir(), "hdm.db"))
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	defer le.Close()
	ns := "urn:hdm:apps:x"
	if err := evolution.SaveContract(le, ns, &evolution.AppContract{Fields: []evolution.ContractField{
		{Name: "player_x", Offset: 0xB0000, Type: "i32"},
		{Name: "score", Offset: 0xB0004, Type: "i32"},
	}}); err != nil {
		t.Fatalf("contract: %v", err)
	}
	// A subsystem whose declared boundary is WRONG for its stated purpose.
	env := &AppEnvelope{ApplicationNamespace: ns, Objective: "obj",
		SubsystemRequirements: []Subsystem{
			{Identity: ns + ":input", Semantics: "read HMI input, update player_x",
				Reads: []string{"score"}, Writes: nil},
		}}
	saveEnvelope(le, env)

	// The model proposes the corrected boundary; "bogus" is not a contract field and
	// must be dropped by grounding.
	g := NewGrower(le, boundaryModel{out: `{"reads":["HMI input"],"writes":["player_x","bogus"]}`})
	changed, err := g.EvolveBoundary(context.Background(), ns, ns+":input", "stalled")
	if err != nil {
		t.Fatalf("evolve: %v", err)
	}
	if !changed {
		t.Fatal("expected the boundary to change")
	}
	got := LoadEnvelope(le, ns).SubsystemRequirements[0]
	if !sameFieldSet(got.Reads, []string{"HMI input"}) {
		t.Errorf("reads=%v, want [HMI input]", got.Reads)
	}
	if !sameFieldSet(got.Writes, []string{"player_x"}) { // "bogus" dropped by grounding
		t.Errorf("writes=%v, want [player_x] (bogus must be dropped)", got.Writes)
	}
	// Idempotent: re-evolving to the same grounded boundary reports no change.
	if changed2, _ := g.EvolveBoundary(context.Background(), ns, ns+":input", "stalled"); changed2 {
		t.Error("a second identical evolve should report no change")
	}
}

// TestBoundaryCheck: a non-empty friction-free boundary is STABLE; friction or a changed
// port set produces a different fingerprint (so the memo re-evaluates).
func TestBoundaryCheck(t *testing.T) {
	le, _ := storage.NewLedgerEngine(filepath.Join(t.TempDir(), "hdm.db"))
	defer le.Close()
	ns := "urn:hdm:apps:z"
	_ = evolution.SaveContract(le, ns, &evolution.AppContract{Fields: []evolution.ContractField{
		{Name: "a", Offset: 0xB0000, Type: "i32"}, {Name: "b", Offset: 0xB0004, Type: "i32"},
	}})
	sub := Subsystem{Identity: ns + ":c", Semantics: "does a->b", Reads: []string{"a"}, Writes: []string{"b"}}
	saveEnvelope(le, &AppEnvelope{ApplicationNamespace: ns, Objective: "obj", SubsystemRequirements: []Subsystem{sub}})
	g := NewGrower(le, boundaryModel{})

	// No app map yet (no verified access) → not deemed stable, but has a fingerprint.
	stable0, fp0 := g.BoundaryCheck(ns, ns+":c")
	if stable0 {
		t.Error("without a verified app map, should not be marked stable")
	}
	if fp0 == "" {
		t.Error("expected a fingerprint")
	}

	// App map where verified access EXACTLY matches the declared boundary → STABLE.
	m := &evolution.AppMap{Namespace: ns, Components: []evolution.ComponentMap{
		{Identity: ns + ":c", DeclaredReads: []string{"a"}, DeclaredWrites: []string{"b"}, Reads: []string{"a"}, Writes: []string{"b"}},
	}}
	_ = evolution.SaveAppMap(le, ns, m)
	if stable, _ := g.BoundaryCheck(ns, ns+":c"); !stable {
		t.Error("declared == verified should be stable")
	}

	// Introduce friction (code writes an undeclared field) → NOT stable, fingerprint changes.
	m.Components[0].Writes = []string{"b", "a"}
	_ = evolution.SaveAppMap(le, ns, m)
	stable2, fp2 := g.BoundaryCheck(ns, ns+":c")
	if stable2 {
		t.Error("friction (undeclared write) should not be stable")
	}
	if fp2 == fp0 {
		t.Error("fingerprint should change when verified access changes")
	}
}

// TestEvolveBoundarySkipsComposition: a composition driver's ports are forced from in/out
// and must never be model-evolved.
func TestEvolveBoundarySkipsComposition(t *testing.T) {
	le, _ := storage.NewLedgerEngine(filepath.Join(t.TempDir(), "hdm.db"))
	defer le.Close()
	ns := "urn:hdm:apps:y"
	_ = evolution.SaveContract(le, ns, &evolution.AppContract{Fields: []evolution.ContractField{{Name: "grid", Offset: 0xB0000, Type: "i32[4]"}}})
	env := &AppEnvelope{ApplicationNamespace: ns,
		SubsystemRequirements: []Subsystem{
			{Identity: ns + ":driver", Semantics: "map", Composition: &Composition{Combinator: "map", Leaf: ns + ":leaf", In: "grid", Out: "grid"}},
		}}
	saveEnvelope(le, env)
	g := NewGrower(le, boundaryModel{out: `{"reads":["grid"],"writes":["grid"]}`})
	if changed, err := g.EvolveBoundary(context.Background(), ns, ns+":driver", "stalled"); err != nil || changed {
		t.Fatalf("composition boundary must not evolve: changed=%v err=%v", changed, err)
	}
}

package appgen

import (
	"context"
	"testing"
)

func TestCanonicalNamespaceCollapsesSeparators(t *testing.T) {
	for in, want := range map[string]string{
		"urn:hdm:apps:space-invaders": "urn:hdm:apps:space_invaders",
		"urn:hdm:apps:Space Invaders": "urn:hdm:apps:space_invaders",
		"urn:hdm:apps:space_invaders": "urn:hdm:apps:space_invaders",
		"urn:hdm:apps:game--of--life": "urn:hdm:apps:game_of_life",
		"urn:hdm:sys:optimizer":       "urn:hdm:sys:optimizer", // non-app: unchanged
	} {
		if got := canonicalNamespace(in); got != want {
			t.Errorf("canonicalNamespace(%q)=%q want %q", in, got, want)
		}
	}
}

// TestCompileEnvelopeRefinesExistingApp: two grows of the same objective (whose
// namespace drifts hyphen↔underscore) resolve to ONE app, and the second updates
// the objective in place rather than duplicating.
func TestCompileEnvelopeRefinesExistingApp(t *testing.T) {
	g, _ := newGrower(t, &seqModel{resp: []string{
		`{"application_namespace":"urn:hdm:apps:space-invaders","global_constraints":{},"subsystem_requirements":[{"identity":"urn:hdm:apps:space-invaders:renderer","semantics":"render"}]}`,
		`{"application_namespace":"urn:hdm:apps:space_invaders","global_constraints":{},"subsystem_requirements":[{"identity":"urn:hdm:apps:space_invaders:renderer","semantics":"render"},{"identity":"urn:hdm:apps:space_invaders:physics","semantics":"physics"}]}`,
	}})
	first, err := g.CompileEnvelope(context.Background(), "space invaders v1")
	if err != nil || first.ApplicationNamespace != "urn:hdm:apps:space_invaders" {
		t.Fatalf("first grow ns=%q err=%v; want canonical space_invaders", first.ApplicationNamespace, err)
	}
	second, err := g.CompileEnvelope(context.Background(), "space invaders v2")
	if err != nil {
		t.Fatalf("second grow: %v", err)
	}
	if second.ApplicationNamespace != "urn:hdm:apps:space_invaders" {
		t.Fatalf("second ns=%q, want same canonical app (no duplicate)", second.ApplicationNamespace)
	}
	// Refine keeps the existing subsystems (1), not the fresh decomposition's 2.
	if len(second.SubsystemRequirements) != 1 {
		t.Fatalf("refine should keep existing subsystems, got %d", len(second.SubsystemRequirements))
	}
	if second.Objective != "space invaders v2" {
		t.Fatalf("refine should update objective, got %q", second.Objective)
	}
}

func TestRefineAndRetireApp(t *testing.T) {
	g, _ := newGrower(t, &seqModel{resp: []string{`{}`}})
	saveEnvelope(g.ledger, &AppEnvelope{
		ApplicationNamespace:  "urn:hdm:apps:demo",
		Objective:             "old",
		SubsystemRequirements: []Subsystem{{Identity: "urn:hdm:apps:demo:r", Semantics: "render"}},
	})
	if !g.AppExists("urn:hdm:apps:demo") {
		t.Fatal("AppExists should be true")
	}
	env, err := g.Refine(context.Background(), "urn:hdm:apps:demo", "new objective")
	if err != nil || env.Objective != "new objective" {
		t.Fatalf("Refine = %+v, %v", env, err)
	}
	if got := LoadEnvelope(g.ledger, "urn:hdm:apps:demo"); got == nil || got.Objective != "new objective" {
		t.Fatalf("refine not persisted: %+v", got)
	}
	// Retire drops the envelope (and any namespaced refs).
	n, err := g.RetireApp("urn:hdm:apps:demo")
	if err != nil || n == 0 {
		t.Fatalf("RetireApp dropped %d refs, err=%v; want >0", n, err)
	}
	if g.AppExists("urn:hdm:apps:demo") {
		t.Fatal("app should be gone after RetireApp")
	}
}

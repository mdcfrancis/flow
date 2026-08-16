package appgen

import (
	"context"
	"testing"

	"github.com/mdcfrancis/flow/evolution"
)

// Two applications must never share a contract arena — that is the whole reason
// arenas exist. Without this, both pack from the same base and overwrite each
// other's live state field for field.
func TestNewAppsGetDistinctArenas(t *testing.T) {
	g, _ := newGrower(t, &seqModel{resp: []string{
		`{"application_namespace":"urn:hdm:apps:alpha","global_constraints":{},"subsystem_requirements":[{"identity":"urn:hdm:apps:alpha:r","semantics":"render"}]}`,
		`{"application_namespace":"urn:hdm:apps:beta","global_constraints":{},"subsystem_requirements":[{"identity":"urn:hdm:apps:beta:r","semantics":"render"}]}`,
	}})
	a, err := g.CompileEnvelope(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("grow alpha: %v", err)
	}
	b, err := g.CompileEnvelope(context.Background(), "beta")
	if err != nil {
		t.Fatalf("grow beta: %v", err)
	}
	if a.Arena() == b.Arena() {
		t.Fatalf("both apps got arena 0x%X — they would overwrite each other", a.Arena())
	}
	if a.Arena() < evolution.FirstArenaBase || b.Arena() < evolution.FirstArenaBase {
		t.Fatalf("arenas must sit above the host-return scratch zone: alpha=0x%X beta=0x%X", a.Arena(), b.Arena())
	}
	// Non-overlapping windows, not merely different bases.
	lo, hi := a.Arena(), b.Arena()
	if lo > hi {
		lo, hi = hi, lo
	}
	if evolution.ArenaEnd(lo) > hi {
		t.Fatalf("arenas overlap: 0x%X..0x%X and 0x%X", lo, evolution.ArenaEnd(lo), hi)
	}
}

// An envelope persisted before arenas existed has ArenaBase 0. Its cells were
// synthesized against the legacy offsets, so it must keep resolving there rather
// than being silently relocated out from under them.
func TestLegacyEnvelopeKeepsLegacyArena(t *testing.T) {
	var env *AppEnvelope
	if env.Arena() != evolution.LegacyArenaBase {
		t.Fatalf("nil envelope arena = 0x%X, want legacy 0x%X", env.Arena(), evolution.LegacyArenaBase)
	}
	env = &AppEnvelope{ApplicationNamespace: "urn:hdm:apps:old"}
	if env.Arena() != evolution.LegacyArenaBase {
		t.Fatalf("legacy envelope arena = 0x%X, want 0x%X", env.Arena(), evolution.LegacyArenaBase)
	}
}

// Retiring an app must release its arena so the window is reused, not leaked —
// otherwise a system that grows and deletes apps runs out after MaxArenas.
func TestRetiredArenaIsReused(t *testing.T) {
	g, _ := newGrower(t, &seqModel{resp: []string{
		`{"application_namespace":"urn:hdm:apps:alpha","global_constraints":{},"subsystem_requirements":[{"identity":"urn:hdm:apps:alpha:r","semantics":"render"}]}`,
		`{"application_namespace":"urn:hdm:apps:gamma","global_constraints":{},"subsystem_requirements":[{"identity":"urn:hdm:apps:gamma:r","semantics":"render"}]}`,
	}})
	a, err := g.CompileEnvelope(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("grow alpha: %v", err)
	}
	first := a.Arena()
	if _, err := g.RetireApp("urn:hdm:apps:alpha"); err != nil {
		t.Fatalf("retire: %v", err)
	}
	c, err := g.CompileEnvelope(context.Background(), "gamma")
	if err != nil {
		t.Fatalf("grow gamma: %v", err)
	}
	if c.Arena() != first {
		t.Fatalf("arena 0x%X was not reused after retire (new app got 0x%X)", first, c.Arena())
	}
}

// RetireApp must unregister the app's cells, not just drop their refs — a stale
// registry entry whose descriptor no longer loads would be selected as an
// evolution candidate forever.
func TestRetireAppUnregistersCells(t *testing.T) {
	g, _ := newGrower(t, &seqModel{resp: []string{`{}`}})
	reg := evolution.NewCellRegistry("urn:hdm:apps:demo:r", "urn:hdm:sys:map")
	g.SetRegistry(reg)
	saveEnvelope(g.ledger, &AppEnvelope{
		ApplicationNamespace:  "urn:hdm:apps:demo",
		SubsystemRequirements: []Subsystem{{Identity: "urn:hdm:apps:demo:r", Semantics: "render"}},
	})
	if _, err := g.RetireApp("urn:hdm:apps:demo"); err != nil {
		t.Fatalf("retire: %v", err)
	}
	for _, u := range reg.List() {
		if u == "urn:hdm:apps:demo:r" {
			t.Fatal("retired app's cell is still registered — it stays an evolution candidate")
		}
	}
	// Unrelated cells must survive.
	found := false
	for _, u := range reg.List() {
		if u == "urn:hdm:sys:map" {
			found = true
		}
	}
	if !found {
		t.Fatal("retire removed a cell belonging to another namespace")
	}
}

// An explicit "new app" must not be swallowed by the refine branch when the model
// reaches for a namespace that is already taken.
func TestCompileEnvelopeNewAvoidsCollision(t *testing.T) {
	sameNS := `{"application_namespace":"urn:hdm:apps:pong","global_constraints":{},"subsystem_requirements":[{"identity":"urn:hdm:apps:pong:r","semantics":"render"}]}`
	g, _ := newGrower(t, &seqModel{resp: []string{sameNS, sameNS}})
	first, err := g.CompileEnvelope(context.Background(), "pong")
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second, err := g.CompileEnvelopeNew(context.Background(), "a different pong")
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if second.ApplicationNamespace == first.ApplicationNamespace {
		t.Fatalf("explicit new app reused namespace %q — it refined instead of creating", first.ApplicationNamespace)
	}
	if second.Objective != "a different pong" {
		t.Fatalf("new app objective = %q", second.Objective)
	}
	if second.Arena() == first.Arena() {
		t.Fatalf("the two apps share arena 0x%X", first.Arena())
	}
	// Subsystems must be rebased under the NEW namespace, not the colliding one.
	for _, s := range second.SubsystemRequirements {
		if got := evolution.AppNamespaceOf(s.Identity); got != second.ApplicationNamespace {
			t.Fatalf("subsystem %q is not under %q", s.Identity, second.ApplicationNamespace)
		}
	}
}

// packOffsets must lay fields out inside whichever arena it is given, and drop
// what spills past that arena's end rather than the legacy one.
func TestPackOffsetsHonoursArena(t *testing.T) {
	base := evolution.ArenaBaseAt(3)
	in := []evolution.ContractField{
		{Name: "a", Type: "i32"},
		{Name: "big", Type: "i32[100000]"}, // 400 KB > 64 KiB arena -> dropped
		{Name: "b", Type: "i32"},
	}
	out := packOffsets(in, base)
	if len(out) != 2 {
		t.Fatalf("got %d fields, want 2 (the oversized one dropped)", len(out))
	}
	if out[0].Offset != base {
		t.Errorf("first field at 0x%X, want arena base 0x%X", out[0].Offset, base)
	}
	for _, f := range out {
		if !evolution.InArena(base, f.Offset) {
			t.Errorf("field %q at 0x%X escaped arena 0x%X..0x%X", f.Name, f.Offset, base, evolution.ArenaEnd(base))
		}
	}
}

package appgen

import "testing"

func TestDeriveKindFromPorts(t *testing.T) {
	cases := []struct {
		name string
		sub  Subsystem
		want CellKind
	}{
		{"physics writes state -> compute",
			Subsystem{Reads: []string{"initialized", "ball_positions"}, Writes: []string{"ball_positions", "ball_velocities"}},
			KindCompute},
		{"renderer read-only -> render",
			Subsystem{Reads: []string{"ball_positions", "ball_colors"}, Writes: nil},
			KindRender},
		{"empty ports, plain semantics -> compute (leaf-ness is a graph fact)",
			Subsystem{Semantics: "double the input", Reads: nil, Writes: nil},
			KindCompute},
		{"empty ports, render-y semantics -> render (keyword tiebreaker)",
			Subsystem{Semantics: "render the view to the canvas", Reads: nil, Writes: nil},
			KindRender},
		{"HMI + writes -> input",
			Subsystem{Reads: []string{"HMI input"}, Writes: []string{"player_x"}},
			KindInput},
		{"composition object -> compose",
			Subsystem{Reads: []string{"grid_coords"}, Writes: []string{"escape_times"}, Composition: &Composition{Combinator: "map"}},
			KindCompose},
	}
	for _, c := range cases {
		if got := deriveKind(c.sub); got != c.want {
			t.Errorf("%s: deriveKind = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestReconcileKindPortsWin(t *testing.T) {
	// The physics bug: model declares render, but the cell writes state → override to compute.
	sub := Subsystem{Kind: KindRender, Reads: []string{"initialized"}, Writes: []string{"ball_positions"}}
	got, overridden := reconcileKind(sub)
	if got != KindCompute || !overridden {
		t.Fatalf("declared render + writes state must override to compute (overridden), got %q overridden=%v", got, overridden)
	}

	// A consistent declaration is honored (no override).
	sub = Subsystem{Kind: KindRender, Reads: []string{"ball_positions"}, Writes: nil}
	if got, over := reconcileKind(sub); got != KindRender || over {
		t.Fatalf("consistent render must stand, got %q over=%v", got, over)
	}

	// No declaration → derive silently.
	sub = Subsystem{Reads: []string{"HMI input"}, Writes: []string{"player_x"}}
	if got, over := reconcileKind(sub); got != KindInput || over {
		t.Fatalf("undeclared HMI writer must derive to input silently, got %q over=%v", got, over)
	}

	// declared input but no HMI → override to compute.
	sub = Subsystem{Kind: KindInput, Reads: []string{"ball_positions"}, Writes: []string{"ball_positions"}}
	if got, over := reconcileKind(sub); got != KindCompute || !over {
		t.Fatalf("declared input without HMI must override to compute, got %q over=%v", got, over)
	}
}

func TestNormalizeKindsLegacyEnvelope(t *testing.T) {
	// A legacy envelope with NO declared kinds gets every subsystem filled from ports.
	env := &AppEnvelope{SubsystemRequirements: []Subsystem{
		{Identity: "urn:x:physics", Reads: []string{"initialized"}, Writes: []string{"ball_positions"}},
		{Identity: "urn:x:renderer", Reads: []string{"ball_positions"}},
		{Identity: "urn:x:input", Reads: []string{"HMI input"}, Writes: []string{"player_x"}},
	}}
	normalizeKinds(env)
	want := []CellKind{KindCompute, KindRender, KindInput}
	for i, w := range want {
		if got := env.SubsystemRequirements[i].Kind; got != w {
			t.Errorf("subsystem %d: kind = %q, want %q", i, got, w)
		}
	}
	// Idempotent.
	normalizeKinds(env)
	if env.SubsystemRequirements[0].Kind != KindCompute {
		t.Fatal("normalizeKinds must be idempotent")
	}
}

func TestNormalizeKindsCompositionLeaf(t *testing.T) {
	// A composition's leaf function (empty ports, referenced by a driver) is
	// authoritatively a leaf — resolved via the graph, not ports/semantics.
	env := &AppEnvelope{SubsystemRequirements: []Subsystem{
		{Identity: "urn:x:escape", Reads: nil, Writes: nil},
		{Identity: "urn:x:grid", Reads: []string{"grid_coords"}, Writes: []string{"escape_times"},
			Composition: &Composition{Combinator: "map", Leaf: "urn:x:escape", In: "grid_coords", Out: "escape_times"}},
	}}
	normalizeKinds(env)
	if got := env.SubsystemRequirements[0].Kind; got != KindLeaf {
		t.Errorf("composition leaf kind = %q, want leaf", got)
	}
	if got := env.SubsystemRequirements[1].Kind; got != KindCompose {
		t.Errorf("composition driver kind = %q, want compose", got)
	}
}

func TestEntryForMatchesKind(t *testing.T) {
	if e := entryFor(Subsystem{Kind: KindCompute}); e != "run-tick" {
		t.Errorf("compute entry = %q, want run-tick", e)
	}
	if e := entryFor(Subsystem{Kind: KindRender, Reads: []string{"x"}}); e != "render-frame" {
		t.Errorf("render entry = %q, want render-frame", e)
	}
}

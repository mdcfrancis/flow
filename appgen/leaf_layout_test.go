package appgen

import (
	"path/filepath"
	"testing"

	"github.com/mdcfrancis/flow/evolution"
	"github.com/mdcfrancis/flow/flux"
	"github.com/mdcfrancis/flow/storage"
)

// A combinator leaf whose per-element state comes from f32[N] contract arrays must get
// FLOAT element fields — so (set vx (f32.add (get vx) …)) type-checks instead of failing
// "expected f32, got i32". This is the fix that lets the nebula's physics_leaf author real
// float physics over its particle instead of falling back to raw i32 (which floors to zero).
func TestLeafElementLayoutFloatFromContract(t *testing.T) {
	le, err := storage.NewLedgerEngine(filepath.Join(t.TempDir(), "hdm.db"))
	if err != nil {
		t.Fatal(err)
	}
	ns := "urn:hdm:apps:nebula"
	leafURN := ns + ":physics_leaf"
	saveEnvelope(le, &AppEnvelope{
		ApplicationNamespace: ns,
		SubsystemRequirements: []Subsystem{
			{Identity: ns + ":physics_map", Composition: &Composition{
				Combinator: "map", Leaf: leafURN, In: "particle_states", Out: "particle_states"}},
			{Identity: leafURN,
				Reads:  []string{"x", "y", "vx", "vy", "attractor_x", "attractor_y"},
				Writes: []string{"x", "y", "vx", "vy"}},
		},
	})
	_ = evolution.SaveContract(le, ns, &evolution.AppContract{Fields: []evolution.ContractField{
		{Name: "x", Offset: 0xB0000, Type: "f32[256]"},
		{Name: "y", Offset: 0xB1000, Type: "f32[256]"},
		{Name: "vx", Offset: 0xB2000, Type: "f32[256]"},
		{Name: "vy", Offset: 0xB3000, Type: "f32[256]"},
		{Name: "attractor_x", Offset: 0xB4000, Type: "f32", Init: 160},
		{Name: "attractor_y", Offset: 0xB4004, Type: "f32", Init: 120},
	}})

	layout := LeafElementLayout(le, leafURN)
	if layout == nil {
		t.Fatal("no element layout produced for the leaf")
	}
	for _, name := range []string{"x", "y", "vx", "vy"} {
		f := layout[name]
		if !f.Elem {
			t.Errorf("%s should be an element (arg-relative) field: %+v", name, f)
		}
		if f.Type != flux.TFloat {
			t.Errorf("%s element must be f32 (sourced from an f32[] array), got %v", name, f.Type)
		}
	}
	// The shared attractor stays an absolute-offset global (not an element field).
	if ax := layout["attractor_x"]; ax.Elem || ax.Type != flux.TFloat {
		t.Errorf("attractor_x should be an absolute f32 global: %+v", ax)
	}
}

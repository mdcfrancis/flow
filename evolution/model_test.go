package evolution

import (
	"path/filepath"
	"testing"

	"github.com/mdcfrancis/flow/storage"
)

func TestSystemModelRoundTrip(t *testing.T) {
	le, err := storage.NewLedgerEngine(filepath.Join(t.TempDir(), "hdm.db"))
	if err != nil {
		t.Fatal(err)
	}
	ns := "urn:hdm:apps:nebula"
	m := &SystemModel{
		Namespace: ns, Objective: "a particle nebula",
		Entities: []Entity{
			{Name: "particle", Cardinality: 100, Purpose: "a drifting speck", Fields: []EntityField{
				{Name: "x", Type: "f32"}, {Name: "vx", Type: "f32"}}},
			{Name: "attractor", Cardinality: 1, Fields: []EntityField{{Name: "x", Type: "f32"}}},
		},
		Dynamics: []string{"each particle: vx += (attractor.x - x)/K; x += vx"},
	}
	if err := SaveModel(le, ns, m); err != nil {
		t.Fatal(err)
	}
	got := LoadModel(le, ns)
	if got == nil || len(got.Entities) != 2 || got.Entities[0].Cardinality != 100 {
		t.Fatalf("round-trip lost the model: %+v", got)
	}
}

// The projection is the ONE rule that turns the model into the contract: a multi-instance
// entity's fields become arrays of its cardinality plus a count; a singleton stays scalar.
// Names and types are canonical — this is what makes drift impossible.
func TestModelProjectsToContract(t *testing.T) {
	m := &SystemModel{Entities: []Entity{
		{Name: "particle", Cardinality: 100, Fields: []EntityField{
			{Name: "x", Type: "f32"}, {Name: "y", Type: "f32"}, {Name: "vx", Type: "f32"}}},
		{Name: "attractor", Cardinality: 1, Fields: []EntityField{
			{Name: "x", Type: "f32"}, {Name: "y", Type: "f32"}}},
		{Name: "screen", Cardinality: 1, Fields: []EntityField{
			{Name: "width", Type: "i32"}}},
	}}
	got := map[string]string{}
	var counts int
	for _, f := range m.ProjectFields() {
		got[f.Name] = f.Type
		if f.Name == "particle_count" {
			counts++
			if f.Init != 100 {
				t.Errorf("particle_count init should equal cardinality, got %d", f.Init)
			}
		}
	}
	want := map[string]string{
		"particle_count": "i32", "particle_x": "f32[100]", "particle_y": "f32[100]", "particle_vx": "f32[100]",
		"attractor_x": "f32", "attractor_y": "f32", "screen_width": "i32",
	}
	for n, ty := range want {
		if got[n] != ty {
			t.Errorf("field %s: got %q want %q", n, got[n], ty)
		}
	}
	if counts != 1 {
		t.Errorf("expected exactly one count field (only for the multi-instance entity), got %d", counts)
	}
	// A singleton must NOT get a count field.
	if _, ok := got["attractor_count"]; ok {
		t.Error("singleton entity should not project a count field")
	}
	// No phantom names — every projected field traces to an entity.field.
	if _, ok := got["particle_states"]; ok {
		t.Error("projection invented a phantom field")
	}
}

// A non-i32/f32 element type is clamped (the substrate only addresses those two).
func TestModelProjectionClampsType(t *testing.T) {
	m := &SystemModel{Entities: []Entity{
		{Name: "cell", Cardinality: 1, Fields: []EntityField{{Name: "flag", Type: "bool"}}},
	}}
	for _, f := range m.ProjectFields() {
		if f.Name == "cell_flag" && f.Type != "i32" {
			t.Fatalf("unknown element type should clamp to i32, got %q", f.Type)
		}
	}
}

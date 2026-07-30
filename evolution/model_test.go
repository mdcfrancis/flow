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

// A collection entity projects to one DENSE array column per field, laid out contiguously
// after its count — the layout a plain looping cell updates in place (no map combinator,
// so no need to interleave records).
func TestModelProjectsCollectionColumns(t *testing.T) {
	m := &SystemModel{Entities: []Entity{
		{Name: "particle", Cardinality: 100, Fields: []EntityField{
			{Name: "x", Type: "f32"}, {Name: "y", Type: "f32"}, {Name: "vx", Type: "f32"}, {Name: "vy", Type: "f32"}}},
		{Name: "attractor", Cardinality: 1, Fields: []EntityField{{Name: "x", Type: "f32"}}},
	}}
	fields := map[string]ContractField{}
	for _, f := range m.ProjectFields() {
		fields[f.Name] = f
	}
	// Each column is a dense f32[100] (400 bytes), contiguous.
	base := fields["particle_x"].Offset
	for j, name := range []string{"particle_x", "particle_y", "particle_vx", "particle_vy"} {
		f := fields[name]
		if f.Offset != base+j*400 {
			t.Errorf("%s offset: got 0x%X want 0x%X (dense columns)", name, f.Offset, base+j*400)
		}
		if f.Stride != 0 {
			t.Errorf("%s must be a dense array (stride 0), got %d", name, f.Stride)
		}
		if f.Type != "f32[100]" {
			t.Errorf("%s type: got %q want f32[100]", name, f.Type)
		}
	}
	// attractor starts after the four particle columns.
	if fields["attractor_x"].Offset != base+4*400 {
		t.Errorf("attractor_x offset: got 0x%X want 0x%X", fields["attractor_x"].Offset, base+4*400)
	}
	// No interleaved-buffer handle or scratch map-out — collections are looped, not mapped.
	if _, ok := fields["particle"]; ok {
		t.Error("dense projection must not emit an interleaved buffer handle")
	}
	if _, ok := fields["particle_mapout"]; ok {
		t.Error("dense projection must not emit a scratch map-out")
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

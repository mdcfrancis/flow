package appgen

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mdcfrancis/flow/evolution"
	"github.com/mdcfrancis/flow/flux"
	"github.com/mdcfrancis/flow/storage"
)

// modelScriptModel returns the model JSON for the model-authoring prompt, "" otherwise.
type modelScriptModel struct{ modelJSON string }

func (m modelScriptModel) InvokeReasoning(ctx context.Context, sys, user string) (string, error) {
	if strings.Contains(sys, "CANONICAL MODEL") {
		return m.modelJSON, nil
	}
	return "", nil
}

const nebulaModelJSON = `{"entities":[
  {"name":"particle","cardinality":100,"purpose":"a drifting speck",
   "fields":[{"name":"x","type":"f32"},{"name":"y","type":"f32"},{"name":"vx","type":"f32"},{"name":"vy","type":"f32"}]},
  {"name":"attractor","cardinality":1,"fields":[{"name":"x","type":"f32"},{"name":"y","type":"f32"}]},
  {"name":"screen","cardinality":1,"fields":[{"name":"width","type":"i32"},{"name":"height","type":"i32"}]}
 ],"dynamics":["each particle: vx += (attractor.x - x)/K; x += vx"]}`

// The model is authored first and the contract is PROJECTED from it — canonical names and
// types, and crucially NO phantom particle_states/attractor_pos fields.
func TestAuthorModelThenProjectContract(t *testing.T) {
	g, _ := newGrower(t, modelScriptModel{modelJSON: nebulaModelJSON})
	saveEnvelope(g.ledger, &AppEnvelope{
		ApplicationNamespace: nebulaNS, Objective: "a glowing particle nebula",
		SubsystemRequirements: []Subsystem{{Identity: nebulaNS + ":renderer", Semantics: "draw particles"}},
	})
	if err := g.AuthorModel(context.Background(), nebulaNS); err != nil {
		t.Fatal(err)
	}
	if m := evolution.LoadModel(g.ledger, nebulaNS); m == nil || len(m.Entities) != 3 {
		t.Fatalf("model not authored: %+v", m)
	}
	if _, err := g.EnsureContract(context.Background(), nebulaNS); err != nil {
		t.Fatal(err)
	}
	c := evolution.LoadContract(g.ledger, nebulaNS)
	if c == nil {
		t.Fatal("no contract projected")
	}
	byName := map[string]string{}
	for _, f := range c.Fields {
		byName[f.Name] = f.Type
	}
	for n, ty := range map[string]string{
		"particle_x": "f32[100]", "particle_vy": "f32[100]", "particle_count": "i32",
		"attractor_x": "f32", "screen_width": "i32",
	} {
		if byName[n] != ty {
			t.Errorf("projected %s: got %q want %q", n, byName[n], ty)
		}
	}
	for _, phantom := range []string{"particle_states", "attractor_pos"} {
		if _, ok := byName[phantom]; ok {
			t.Errorf("phantom field %q appeared in the projected contract", phantom)
		}
	}
	// Every field is placed in the sandbox region with a real offset.
	for _, f := range c.Fields {
		if f.Offset < 0xB0000 || f.Offset >= 0xC0000 {
			t.Errorf("field %s offset 0x%X out of region", f.Name, f.Offset)
		}
	}
}

// A component's ports are resolved to canonical model fields: an entity reference expands,
// a bare field is prefixed, and a token that names nothing modeled is dropped (never minted
// as a phantom field).
func TestResolvePortsToModel(t *testing.T) {
	g, _ := newGrower(t, modelScriptModel{})
	_ = evolution.SaveModel(g.ledger, nebulaNS, &evolution.SystemModel{Namespace: nebulaNS, Entities: []evolution.Entity{
		{Name: "particle", Cardinality: 100, Fields: []evolution.EntityField{
			{Name: "x", Type: "f32"}, {Name: "y", Type: "f32"}, {Name: "vx", Type: "f32"}, {Name: "vy", Type: "f32"}}},
		{Name: "attractor", Cardinality: 1, Fields: []evolution.EntityField{{Name: "x", Type: "f32"}, {Name: "y", Type: "f32"}}},
	}})
	saveEnvelope(g.ledger, &AppEnvelope{
		ApplicationNamespace: nebulaNS,
		SubsystemRequirements: []Subsystem{{Identity: nebulaNS + ":physics_leaf",
			Writes: []string{"particle_states", "vx"},          // whole-entity + bare field
			Reads:  []string{"attractor", "bogus_field", "vx"}}, // entity + junk + bare
		},
	})
	g.resolvePortsToModel(nebulaNS)
	env := LoadEnvelope(g.ledger, nebulaNS)
	leaf := env.SubsystemRequirements[0]
	// Writes: particle_states → all 4 particle fields; bare vx → particle_vx (deduped).
	wantW := map[string]bool{"particle_x": true, "particle_y": true, "particle_vx": true, "particle_vy": true}
	if len(leaf.Writes) != 4 {
		t.Fatalf("writes not resolved cleanly: %v", leaf.Writes)
	}
	for _, w := range leaf.Writes {
		if !wantW[w] {
			t.Errorf("unexpected resolved write %q", w)
		}
	}
	// Reads: attractor → attractor_x/y; vx → particle_vx; bogus_field dropped.
	for _, junk := range leaf.Reads {
		if junk == "bogus_field" {
			t.Error("a port naming nothing modeled must be dropped, not kept")
		}
	}
	if !contains(leaf.Reads, "attractor_x") || !contains(leaf.Reads, "particle_vx") {
		t.Errorf("reads not resolved: %v", leaf.Reads)
	}
}

// The leaf's element layout comes from the model's collection entity — f32 element fields
// for continuous particle state — regardless of how the leaf's ports were spelled.
func TestLeafElementLayoutFromModel(t *testing.T) {
	le, err := storage.NewLedgerEngine(filepath.Join(t.TempDir(), "hdm.db"))
	if err != nil {
		t.Fatal(err)
	}
	leafURN := nebulaNS + ":physics_leaf"
	_ = evolution.SaveModel(le, nebulaNS, &evolution.SystemModel{Namespace: nebulaNS, Entities: []evolution.Entity{
		{Name: "particle", Cardinality: 100, Fields: []evolution.EntityField{
			{Name: "x", Type: "f32"}, {Name: "y", Type: "f32"}, {Name: "vx", Type: "f32"}, {Name: "vy", Type: "f32"}}},
		{Name: "attractor", Cardinality: 1, Fields: []evolution.EntityField{{Name: "x", Type: "f32"}, {Name: "y", Type: "f32"}}},
	}})
	saveEnvelope(le, &AppEnvelope{
		ApplicationNamespace: nebulaNS,
		SubsystemRequirements: []Subsystem{
			{Identity: nebulaNS + ":physics_map", Composition: &Composition{Combinator: "map", Leaf: leafURN}},
			{Identity: leafURN, Reads: []string{"attractor_x", "attractor_y"}, Writes: []string{"particle_x"}},
		},
	})
	// The projected contract must exist for the global (attractor) resolution.
	g := NewGrower(le, modelScriptModel{})
	if _, err := g.EnsureContract(context.Background(), nebulaNS); err != nil {
		t.Fatal(err)
	}
	layout := LeafElementLayout(le, leafURN)
	if layout == nil {
		t.Fatal("no element layout")
	}
	for _, n := range []string{"particle_x", "particle_y", "particle_vx", "particle_vy"} {
		f := layout[n]
		if !f.Elem || f.Type != flux.TFloat {
			t.Errorf("%s must be an f32 element field, got %+v", n, f)
		}
	}
	// The singleton attractor is an absolute global, not an element.
	if ax := layout["attractor_x"]; ax.Elem {
		t.Errorf("attractor_x should be an absolute global, not an element: %+v", ax)
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

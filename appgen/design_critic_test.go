package appgen

import (
	"context"
	"strings"
	"testing"

	"github.com/mdcfrancis/flow/evolution"
)

// designCriticModel scripts the two design critics by their system-prompt persona.
type designCriticModel struct{ typeJSON, archJSON string }

func (m designCriticModel) InvokeReasoning(ctx context.Context, sys, user string) (string, error) {
	switch {
	case strings.Contains(sys, "TYPE reviewer"):
		return m.typeJSON, nil
	case strings.Contains(sys, "SOFTWARE ARCHITECT"):
		return m.archJSON, nil
	}
	return "", nil
}

const nebulaNS = "urn:hdm:apps:nebula"

func seedNebula(g *Grower) {
	saveEnvelope(g.ledger, &AppEnvelope{
		ApplicationNamespace: nebulaNS,
		Objective:            "a glowing particle nebula drifting toward an attractor",
		SubsystemRequirements: []Subsystem{
			{Identity: nebulaNS + ":physics_leaf",
				Reads:  []string{"particle_x", "particle_vx", "attractor_x"},
				Writes: []string{"particle_x", "particle_vx"}},
			{Identity: nebulaNS + ":renderer",
				Reads: []string{"particle_x", "count"}},
		},
	})
	_ = evolution.SaveContract(g.ledger, nebulaNS, &evolution.AppContract{Fields: []evolution.ContractField{
		{Name: "count", Offset: 0xB0000, Type: "i32", Init: 128},
		{Name: "particle_x", Offset: 0xB0100, Type: "i32[256]", Desc: "each particle's x position"},
		{Name: "particle_vx", Offset: 0xB0500, Type: "i32[256]", Desc: "each particle's x velocity"},
		{Name: "attractor_x", Offset: 0xB0900, Type: "i32", Desc: "gravity well x"},
	}})
}

// The type critic moves continuous particle state off i32 (where gravity floors to zero)
// onto f32, preserving array arity, and REPAIRS the persisted contract.
func TestTypeCriticRepairsContract(t *testing.T) {
	m := designCriticModel{typeJSON: `{"changes":[
	  {"field":"particle_x","from":"i32[256]","to":"f32[256]","reason":"integrated position, must be continuous"},
	  {"field":"particle_vx","from":"i32[256]","to":"f32[256]","reason":"velocity accumulates fractional gravity"},
	  {"field":"attractor_x","from":"i32","to":"f32","reason":"used in fractional gravity math"},
	  {"field":"count","from":"i32","to":"i32[10]","reason":"BOGUS arity change — must be rejected"},
	  {"field":"ghost","from":"i32","to":"f32","reason":"not a real field — must be rejected"}
	]}`}
	g, _ := newGrower(t, m)
	seedNebula(g)

	reps, err := g.critiqueTypes(context.Background(), nebulaNS)
	if err != nil {
		t.Fatal(err)
	}
	if len(reps) != 3 {
		t.Fatalf("expected 3 applied repairs (2 arrays + 1 scalar), got %d: %v", len(reps), reps)
	}
	c := evolution.LoadContract(g.ledger, nebulaNS)
	want := map[string]string{"particle_x": "f32[256]", "particle_vx": "f32[256]", "attractor_x": "f32", "count": "i32"}
	for _, f := range c.Fields {
		if want[f.Name] != f.Type {
			t.Errorf("%s: got type %q want %q", f.Name, f.Type, want[f.Name])
		}
	}
}

// The type critic must not touch a contract that is already type-coherent.
func TestTypeCriticNoOpWhenCoherent(t *testing.T) {
	g, _ := newGrower(t, designCriticModel{typeJSON: `{"changes":[]}`})
	seedNebula(g)
	reps, err := g.critiqueTypes(context.Background(), nebulaNS)
	if err != nil {
		t.Fatal(err)
	}
	if len(reps) != 0 {
		t.Fatalf("expected no repairs, got %v", reps)
	}
}

// The architecture critic repairs the PLAN (steps/interactions/invariants + a Notes reason)
// but never rewrites the ground-truth ports/entry — those are re-anchored from the envelope.
func TestArchitectureCriticRepairsPlan(t *testing.T) {
	m := designCriticModel{archJSON: `{"findings":[
	  {"component":"urn:hdm:apps:nebula:physics_leaf",
	   "problem":"a map leaf must compute one element via run-tick, not render",
	   "revised":{"purpose":"integrate one particle toward the attractor",
	     "steps":["add (attractor_x - particle_x)/K to particle_vx","add particle_vx to particle_x"],
	     "interactions":["hands updated particle_x to the renderer"],
	     "invariants":["particle_x stays on screen"]}}
	]}`}
	g, _ := newGrower(t, m)
	seedNebula(g)
	// Author a plan skeleton with a DELIBERATELY WRONG entry on the leaf.
	_ = evolution.SavePlan(g.ledger, nebulaNS, &evolution.AppPlan{
		Namespace: nebulaNS, Objective: "nebula",
		Components: []evolution.ComponentPlan{
			{Identity: nebulaNS + ":physics_leaf", Purpose: "old", Entry: "render-frame",
				Reads: []string{"x"}, Writes: []string{"x"}, Steps: []string{"draw a rectangle"}},
			{Identity: nebulaNS + ":renderer", Purpose: "draw", Entry: "render-frame"},
		},
	})

	reps, err := g.critiqueArchitecture(context.Background(), nebulaNS)
	if err != nil {
		t.Fatal(err)
	}
	if len(reps) != 1 {
		t.Fatalf("expected 1 architecture repair, got %d: %v", len(reps), reps)
	}
	p := evolution.LoadPlan(g.ledger, nebulaNS)
	leaf := p.Component(nebulaNS + ":physics_leaf")
	if leaf == nil {
		t.Fatal("leaf plan vanished")
	}
	if len(leaf.Steps) != 2 || strings.Contains(strings.Join(leaf.Steps, " "), "rectangle") {
		t.Errorf("steps not revised: %v", leaf.Steps)
	}
	if !strings.Contains(leaf.Notes, "run-tick") {
		t.Errorf("problem not recorded in Notes: %q", leaf.Notes)
	}
	// Ports/entry are re-grounded from the envelope (the leaf has no envelope entry override
	// here, so entryFor decides) — the critic's free-text must not have rewritten the ports.
	if len(leaf.Reads) != 3 { // particle_x, particle_vx, attractor_x from the envelope
		t.Errorf("ports not re-grounded from envelope: reads=%v", leaf.Reads)
	}
}

// modelCriticReasoner scripts the model-conformance critic only.
type modelCriticReasoner struct{ criticJSON string }

func (m modelCriticReasoner) InvokeReasoning(ctx context.Context, sys, user string) (string, error) {
	if strings.Contains(sys, "reviewer of an application's CANONICAL MODEL") {
		return m.criticJSON, nil
	}
	return "", nil
}

// When a model exists, CritiqueDesign reasons from the MODEL: a mistyped field (velocity as
// i32) is retyped in the model, and the correction PROPAGATES through the re-projected
// contract — one source of truth, fixed once.
func TestModelConformanceCriticRetypesAndReprojects(t *testing.T) {
	g, _ := newGrower(t, modelCriticReasoner{criticJSON: `{"changes":[
	  {"kind":"retype","entity":"particle","field":"vx","to":"f32","reason":"velocity is integrated — must be continuous"}
	]}`})
	_ = evolution.SaveModel(g.ledger, nebulaNS, &evolution.SystemModel{
		Namespace: nebulaNS, Objective: "a particle nebula",
		Entities: []evolution.Entity{
			{Name: "particle", Cardinality: 100, Fields: []evolution.EntityField{
				{Name: "x", Type: "f32"}, {Name: "vx", Type: "i32"}}},
		},
		Dynamics: []string{"each particle: vx += force; x += vx"},
	})
	saveEnvelope(g.ledger, &AppEnvelope{ApplicationNamespace: nebulaNS,
		SubsystemRequirements: []Subsystem{{Identity: nebulaNS + ":physics", Writes: []string{"particle_vx"}}}})

	reps, err := g.CritiqueDesign(context.Background(), nebulaNS)
	if err != nil {
		t.Fatal(err)
	}
	// The retype repair must be present (the critic also backfills the collection's missing
	// initial conditions, so there may be more than one repair).
	sawRetype := false
	for _, r := range reps {
		if r.Category == "model" && r.Target == "particle.vx" && r.After == "f32" {
			sawRetype = true
		}
	}
	if !sawRetype {
		t.Fatalf("expected the particle.vx retype repair, got %v", reps)
	}
	m := evolution.LoadModel(g.ledger, nebulaNS)
	if fieldType(m, "particle", "vx") != "f32" {
		t.Errorf("model field particle.vx not retyped to f32")
	}
	c := evolution.LoadContract(g.ledger, nebulaNS)
	if c == nil {
		t.Fatal("no re-projected contract")
	}
	for _, f := range c.Fields {
		if f.Name == "particle_vx" && f.Type != "f32[100]" {
			t.Errorf("re-projected particle_vx: got %q want f32[100]", f.Type)
		}
	}
}

func fieldType(m *evolution.SystemModel, entity, field string) string {
	for _, e := range m.Entities {
		if e.Name == entity {
			for _, f := range e.Fields {
				if f.Name == field {
					return f.Type
				}
			}
		}
	}
	return ""
}

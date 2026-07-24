package appgen

import (
	"context"
	"testing"

	"github.com/mdcfrancis/flow/evolution"
)

// TestFractureCellDecomposesStuckSubsystem verifies the stall-recovery move: a
// stuck subsystem is replaced in the envelope by its sub-cells, the sub-cells are
// scaffolded + enrolled, and the parent is retired.
func TestFractureCellDecomposesStuckSubsystem(t *testing.T) {
	// resp[0] is the fracture verdict; later scaffold calls (wit/genesis/
	// acceptance) reuse the last response and fall back to safe skeletons.
	g, _ := newGrower(t, &seqModel{resp: []string{
		`{"splittable": true, "subsystems": [
		   {"identity":"urn:hdm:apps:si:player_motion","semantics":"read player_input, update player_x"},
		   {"identity":"urn:hdm:apps:si:bullet_system","semantics":"advance bullet_y, set bullet_active"}
		 ]}`,
		`{}`,
	}})
	saveEnvelope(g.ledger, &AppEnvelope{
		ApplicationNamespace: "urn:hdm:apps:si",
		Objective:            "space invaders",
		SubsystemRequirements: []Subsystem{
			{Identity: "urn:hdm:apps:si:renderer", Semantics: "render the scene"},
			{Identity: "urn:hdm:apps:si:physics", Semantics: "player movement, bullets, and fleet"},
		},
	})

	var enrolled, retired []string
	n, err := g.FractureCell(context.Background(), "urn:hdm:apps:si:physics",
		func(u string) { enrolled = append(enrolled, u) },
		func(u string) { retired = append(retired, u) })
	if err != nil || n != 2 {
		t.Fatalf("FractureCell = %d, %v; want 2, nil", n, err)
	}
	if len(enrolled) != 2 {
		t.Fatalf("enrolled = %v, want 2 children", enrolled)
	}
	if len(retired) != 1 || retired[0] != "urn:hdm:apps:si:physics" {
		t.Fatalf("retired = %v, want [physics]", retired)
	}

	// Envelope now holds renderer + the two children, NOT the retired parent.
	env := LoadEnvelope(g.ledger, "urn:hdm:apps:si")
	got := map[string]bool{}
	for _, s := range env.SubsystemRequirements {
		got[s.Identity] = true
	}
	if got["urn:hdm:apps:si:physics"] {
		t.Error("parent physics should be removed from the envelope")
	}
	if !got["urn:hdm:apps:si:renderer"] || len(env.SubsystemRequirements) != 3 {
		t.Fatalf("envelope subsystems = %+v, want renderer + 2 children", env.SubsystemRequirements)
	}
	// Each child was scaffolded to a live descriptor.
	for _, u := range enrolled {
		if _, err := g.repo.Load(u); err != nil {
			t.Errorf("child %s not scaffolded: %v", u, err)
		}
	}
}

// TestAuthorCoordinationProducesScenarios verifies a fractured child is handed
// contract-aware coordination scenarios (proposed then adversarially certified),
// not left a purposeless 0/0 cell.
func TestAuthorCoordinationProducesScenarios(t *testing.T) {
	g, _ := newGrower(t, &seqModel{resp: []string{
		// propose: one coordination scenario reading a shared field
		`{"scenarios":[{"name":"moves_right","seed":[{"at":"0xB0004","u32":[1]}],"expect":{"reads":[{"at":"0xB0000","u32":[5]}]}}]}`,
		// certify: faithful
		`{"verdicts":[{"name":"moves_right","faithful":true,"reason":"player_input=1 -> player_x increases"}]}`,
	}})
	c := &evolution.AppContract{Fields: []evolution.ContractField{
		{Name: "player_x", Offset: 0xB0000, Type: "i32"},
		{Name: "player_input", Offset: 0xB0004, Type: "i32"},
	}}
	suite := g.authorCoordination(context.Background(), "update player_x from player_input", c, "urn:hdm:apps:si", false, []string{"player_x"}, nil)
	if suite == nil {
		t.Fatal("authorCoordination returned nil")
	}
	names := map[string]bool{}
	for _, s := range suite.Scenarios {
		names[s.Name] = true
	}
	// The model's coordination scenario is kept...
	if !names["moves_right"] {
		t.Fatalf("model coordination scenario 'moves_right' missing: %+v", suite.Scenarios)
	}
	// ...and a DIRECTIONAL motion scenario is generated for the written state field
	// (so a state cell is graded on behavior, not its return value).
	if !names["moves_player_x"] {
		t.Fatalf("directional motion scenario 'moves_player_x' missing: %+v", suite.Scenarios)
	}
	// A state-writing cell must not carry scalar int-in/int-out tests.
	if len(suite.Tests) != 0 {
		t.Fatalf("state-writing cell must drop scalar tests, got %+v", suite.Tests)
	}
}

// TestFractureCellAtomicIsNoOp verifies a subsystem judged un-splittable is left
// intact (no children, parent kept).
func TestFractureCellAtomicIsNoOp(t *testing.T) {
	g, _ := newGrower(t, &seqModel{resp: []string{`{"splittable": false}`}})
	saveEnvelope(g.ledger, &AppEnvelope{
		ApplicationNamespace: "urn:hdm:apps:si",
		Objective:            "space invaders",
		SubsystemRequirements: []Subsystem{
			{Identity: "urn:hdm:apps:si:physics", Semantics: "one atomic thing"},
		},
	})
	n, err := g.FractureCell(context.Background(), "urn:hdm:apps:si:physics", func(string) {}, func(string) {})
	if err != nil || n != 0 {
		t.Fatalf("atomic FractureCell = %d, %v; want 0, nil", n, err)
	}
	if env := LoadEnvelope(g.ledger, "urn:hdm:apps:si"); len(env.SubsystemRequirements) != 1 {
		t.Fatalf("envelope changed on atomic no-op: %+v", env.SubsystemRequirements)
	}
}

// TestFractureCellUnknownCellIsNoOp: a URN with no envelope subsystem match.
func TestFractureCellUnknownCellIsNoOp(t *testing.T) {
	g, _ := newGrower(t, &seqModel{resp: []string{`{"splittable": true, "subsystems": []}`}})
	n, err := g.FractureCell(context.Background(), "urn:hdm:apps:none:x", func(string) {}, func(string) {})
	if err != nil || n != 0 {
		t.Fatalf("unknown FractureCell = %d, %v; want 0, nil", n, err)
	}
	_ = evolution.AppNamespaceOf // keep import
}

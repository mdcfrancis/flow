package appgen

import (
	"context"
	"testing"

	"github.com/mdcfrancis/flow/evolution"
)

func TestEnsureContractAuthorsOnceFromEnvelope(t *testing.T) {
	g, _ := newGrower(t, &seqModel{resp: []string{
		`{"fields":[{"name":"player_x","offset":720896,"type":"i32","desc":"player x 0..319"},
		            {"name":"bad","offset":4096,"type":"i32","desc":"out of sandbox — dropped"}]}`,
	}})
	saveEnvelope(g.ledger, &AppEnvelope{
		ApplicationNamespace: "urn:hdm:apps:si",
		Objective:            "space invaders",
		SubsystemRequirements: []Subsystem{
			{Identity: "urn:hdm:apps:si:renderer", Semantics: "render the scene"},
			{Identity: "urn:hdm:apps:si:input", Semantics: "handle keyboard"},
		},
	})

	authored, err := g.EnsureContract(context.Background(), "urn:hdm:apps:si")
	if err != nil || !authored {
		t.Fatalf("EnsureContract: authored=%v err=%v, want true/nil", authored, err)
	}
	c := evolution.LoadContract(g.ledger, "urn:hdm:apps:si")
	if c == nil || len(c.Fields) != 1 || c.Fields[0].Name != "player_x" {
		// the out-of-sandbox "bad" field must be filtered out
		t.Fatalf("contract = %+v, want only player_x (sandbox-valid)", c)
	}
	// Idempotent: a second call authors nothing.
	if authored, _ := g.EnsureContract(context.Background(), "urn:hdm:apps:si"); authored {
		t.Fatal("second EnsureContract should be a no-op")
	}
}

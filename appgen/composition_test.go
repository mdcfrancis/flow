package appgen

import (
	"context"
	"testing"

	"github.com/mdcfrancis/flow/evolution"
)

// TestScaffoldCompositionGeneratesDriver: a composition subsystem yields a
// GENERATED, compiling driver cell wired to the contract arrays — no model synthesis.
func TestScaffoldCompositionGeneratesDriver(t *testing.T) {
	g, repo := newGrower(t, constModel{})
	ns := "urn:hdm:apps:frac"
	if err := evolution.SaveContract(g.ledger, ns, &evolution.AppContract{Fields: []evolution.ContractField{
		{Name: "grid_coords", Offset: 0xB0000, Type: "i32[8]"},  // 4 points x 2 words
		{Name: "escape_times", Offset: 0xB0020, Type: "i32[4]"}, // 4 results
	}}); err != nil {
		t.Fatalf("save contract: %v", err)
	}
	env := &AppEnvelope{ApplicationNamespace: ns}
	sub := Subsystem{Identity: ns + ":grid", Composition: &Composition{
		Combinator: "map", Leaf: ns + ":escape", In: "grid_coords", Out: "escape_times", ElemWords: 2,
	}}
	if err := g.scaffoldComposition(context.Background(), env, sub); err != nil {
		t.Fatalf("scaffoldComposition: %v", err)
	}
	desc, err := repo.Load(sub.Identity)
	if err != nil {
		t.Fatalf("driver not registered: %v", err)
	}
	if _, err := repo.Phenotype(desc); err != nil {
		t.Fatalf("driver phenotype failed to resolve/compile: %v", err)
	}
}

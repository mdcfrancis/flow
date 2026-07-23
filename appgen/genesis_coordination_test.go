package appgen

import (
	"context"
	"strings"
	"testing"

	"github.com/mdcfrancis/flow/evolution"
)

// purposeModel routes canned responses by the system prompt's role, so a scaffold
// exercises the real branch logic (genesis vs scenario authoring vs audit).
type purposeModel struct{ skeleton string }

func (m *purposeModel) InvokeReasoning(ctx context.Context, sys, user string) (string, error) {
	switch {
	case strings.Contains(sys, "acceptance SCENARIOS"): // scenarioPrompt (proposeAdditional)
		return `{"scenarios":[{"name":"moves_right","seed":[{"at":"0xB0004","u32":[1]}],"expect":{"reads":[{"at":"0xB0000","u32":[5]}]}}]}`, nil
	case strings.Contains(sys, "acceptance-test auditor"): // SuiteAuditPrompt (ValidateSuite)
		return `{"verdicts":[{"name":"moves_right","faithful":true,"reason":"input 1 moves player_x"}]}`, nil
	case strings.Contains(sys, "acceptance test cases"): // acceptancePrompt (scalar) — must NOT be used here
		return `{"tests":[{"name":"scalar","input":1,"expected":2}]}`, nil
	default: // wit + genesis
		return m.skeleton, nil
	}
}

// TestScaffoldComputeCellGetsCoordinationScenarios verifies the genesis fix: a
// compute cell in an app that has a shared-state contract is scaffolded with
// contract-aware coordination scenarios (reads-based), NOT contrived scalar
// int-in/int-out tests.
func TestScaffoldComputeCellGetsCoordinationScenarios(t *testing.T) {
	g, _ := newGrower(t, &purposeModel{skeleton: goodSkeleton})
	const ns = "urn:hdm:apps:si"
	if err := evolution.SaveContract(g.ledger, ns, &evolution.AppContract{Fields: []evolution.ContractField{
		{Name: "player_x", Offset: 0xB0000, Type: "i32"},
		{Name: "player_input", Offset: 0xB0004, Type: "i32"},
	}}); err != nil {
		t.Fatalf("save contract: %v", err)
	}
	sub := Subsystem{Identity: ns + ":physics", Semantics: "update player_x based on player_input"}
	env := &AppEnvelope{ApplicationNamespace: ns, Objective: "space invaders", SubsystemRequirements: []Subsystem{sub}}

	if err := g.scaffold(context.Background(), env, sub); err != nil {
		t.Fatalf("scaffold: %v", err)
	}
	su, _ := evolution.LoadAcceptance(g.ledger, sub.Identity)
	if su == nil || len(su.Scenarios) == 0 {
		t.Fatalf("expected coordination scenarios, got %+v", su)
	}
	if len(su.Tests) > 0 {
		t.Fatalf("a contract-bearing compute cell must NOT get scalar tests, got %d", len(su.Tests))
	}
	if len(su.Scenarios[0].Expect.Reads) == 0 {
		t.Fatalf("coordination scenario should carry a reads postcondition: %+v", su.Scenarios[0])
	}
}

// TestScaffoldComputeCellNoContractGetsScalarTests verifies the fallback: with no
// contract, a compute cell still gets scalar tests (unchanged behavior).
func TestScaffoldComputeCellNoContractGetsScalarTests(t *testing.T) {
	g, _ := newGrower(t, &purposeModel{skeleton: goodSkeleton})
	const ns = "urn:hdm:apps:plain"
	sub := Subsystem{Identity: ns + ":calc", Semantics: "double the input"}
	env := &AppEnvelope{ApplicationNamespace: ns, Objective: "calculator", SubsystemRequirements: []Subsystem{sub}}
	if err := g.scaffold(context.Background(), env, sub); err != nil {
		t.Fatalf("scaffold: %v", err)
	}
	su, _ := evolution.LoadAcceptance(g.ledger, sub.Identity)
	if su == nil || len(su.Tests) == 0 {
		t.Fatalf("no-contract compute cell should get scalar tests, got %+v", su)
	}
}

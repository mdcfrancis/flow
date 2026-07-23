package evolution

import (
	"context"
	"errors"
	"testing"
)

func names(s *AcceptanceSuite) map[string]bool {
	m := map[string]bool{}
	for _, t := range s.Tests {
		m[t.Name] = true
	}
	for _, sc := range s.Scenarios {
		m[sc.Name] = true
	}
	return m
}

func TestValidateSuiteDropsOnlyExplicitRejections(t *testing.T) {
	suite := &AcceptanceSuite{Tests: []AcceptanceTest{
		{Name: "double", Input: 2, Expected: 4},
		{Name: "wrong", Input: 2, Expected: 99}, // not faithful to "double the input"
		{Name: "omitted", Input: 3, Expected: 6},
	}}
	// Auditor rejects "wrong", approves "double", and OMITS "omitted".
	audit := `{"verdicts":[
	  {"name":"double","faithful":true,"reason":"correct"},
	  {"name":"wrong","faithful":false,"reason":"2*2=4 not 99"}
	]}`
	model := &fakeReasoner{responses: []string{audit}}

	kept, verdicts, err := ValidateSuite(context.Background(), model, "double the input", suite)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	got := names(kept)
	if got["wrong"] {
		t.Fatalf("explicitly-rejected test 'wrong' must be dropped: %v", got)
	}
	if !got["double"] {
		t.Fatalf("approved test 'double' must be kept: %v", got)
	}
	// Retention-on-omission: an unjudged check is NOT silently lost.
	if !got["omitted"] {
		t.Fatalf("omitted test must be retained (removal needs an explicit rejection): %v", got)
	}
	if len(verdicts) != 2 {
		t.Fatalf("verdicts = %d, want 2", len(verdicts))
	}
}

func TestValidateSuiteFailOpenOnError(t *testing.T) {
	suite := &AcceptanceSuite{Tests: []AcceptanceTest{{Name: "a", Input: 1, Expected: 1}}}
	model := &fakeReasoner{err: errors.New("engine offline")}
	kept, _, err := ValidateSuite(context.Background(), model, "identity", suite)
	if err == nil {
		t.Fatal("expected a transport error")
	}
	// On error the input suite is returned unchanged — never lose tests to a fault.
	if len(kept.Tests) != 1 || kept.Tests[0].Name != "a" {
		t.Fatalf("fail-open violated: kept=%+v", kept)
	}
}

func TestValidateSuiteScenariosByName(t *testing.T) {
	l1 := 1
	suite := &AcceptanceSuite{Scenarios: []Scenario{
		{Name: "moves-right", Steps: 2, Entry: "render-frame",
			Expect: ScenarioExpect{Draw: &DrawExpect{Layer: &l1, Moved: "right"}}},
		{Name: "bogus", Steps: 1, Entry: "render-frame",
			Expect: ScenarioExpect{Draw: &DrawExpect{MinRecords: 999}}},
	}}
	audit := `{"verdicts":[
	  {"name":"moves-right","faithful":true,"reason":"matches 'arrow moves player'"},
	  {"name":"bogus","faithful":false,"reason":"999 primitives not implied"}
	]}`
	kept, _, err := ValidateSuite(context.Background(), &fakeReasoner{responses: []string{audit}}, "arrow key moves the player right", suite)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	got := names(kept)
	if got["bogus"] || !got["moves-right"] {
		t.Fatalf("scenario audit wrong: %v", got)
	}
}

// TestValidateSuiteKeepsCoordinationScenarios verifies a reads-based coordination
// scenario is NEVER dropped by the audit, even on an explicit faithful:false —
// its exact contract-field assertion is the spec, not a requirement-derived
// claim. (Without this, the auditor eroded coordination cells to a vacuous 0/0.)
func TestValidateSuiteKeepsCoordinationScenarios(t *testing.T) {
	suite := &AcceptanceSuite{
		Tests: []AcceptanceTest{{Name: "scalar-bad", Input: 1, Expected: 99}},
		Scenarios: []Scenario{{
			Name: "player_moves_right", Entry: "run-tick",
			Seed:   []SeedWrite{{At: "0xB0004", U32: []uint32{1}}},
			Expect: ScenarioExpect{Reads: []SeedWrite{{At: "0xB0000", U32: []uint32{5}}}},
		}},
	}
	// Auditor rejects BOTH — the scalar test and the coordination scenario.
	audit := `{"verdicts":[
	  {"name":"scalar-bad","faithful":false,"reason":"arbitrary"},
	  {"name":"player_moves_right","faithful":false,"reason":"exact value 5 not justified"}
	]}`
	kept, _, err := ValidateSuite(context.Background(), &fakeReasoner{responses: []string{audit}}, "update player_x from player_input", suite)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	got := names(kept)
	if got["scalar-bad"] {
		t.Error("scalar test rejected by audit should be dropped")
	}
	if !got["player_moves_right"] {
		t.Error("coordination scenario (reads-based) must be KEPT despite rejection")
	}
}

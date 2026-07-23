package evolution

import (
	"strings"
	"testing"
)

func testContract() *AppContract {
	return &AppContract{Fields: []ContractField{
		{Name: "player_x", Offset: 0xB0000, Type: "i32"},
		{Name: "bullet_active", Offset: 0xB0008, Type: "i32"},
		{Name: "invader_alive", Offset: 0xB0020, Type: "i32[4]"}, // spans 0xB0020..0xB002F
	}}
}

// The map's facts must come from the CODE, not a claim: a genotype that stores
// to player_x and loads the HMI key register is reported as writing player_x and
// reading HMI input — in both s-expression and linear WAT form.
func TestScanMemoryAccessDerivesReadsAndWrites(t *testing.T) {
	c := testContract()

	sexpr := `(module
	  (func (export "run-tick") (param i32 i32) (result i32)
	    (i32.store (i32.const 720896) (i32.const 5))      ;; write player_x
	    (drop (i32.load (i32.const 327712)))              ;; read HMI keyCode 0x50020
	    (i32.const 0)))`
	reads, writes := ScanMemoryAccess(sexpr, c)
	if !has(writes, "player_x") {
		t.Errorf("s-expr: writes = %v, want player_x", writes)
	}
	if !has(reads, "HMI input") {
		t.Errorf("s-expr: reads = %v, want HMI input", reads)
	}

	// Linear/stack form: addr const, value, then the store.
	linear := `(module (func (export "run-tick") (param i32 i32) (result i32)
	   i32.const 720896
	   i32.const 7
	   i32.store
	   i32.const 720904
	   i32.load
	   drop
	   i32.const 0))`
	reads, writes = ScanMemoryAccess(linear, c)
	if !has(writes, "player_x") {
		t.Errorf("linear: writes = %v, want player_x", writes)
	}
	if !has(reads, "bullet_active") {
		t.Errorf("linear: reads = %v, want bullet_active", reads)
	}
}

// An array field's extent covers all its slots.
func TestFieldAtOffsetCoversArrays(t *testing.T) {
	c := testContract()
	if got := FieldAtOffset(c, "0xB0028"); got != "invader_alive" { // 3rd slot
		t.Fatalf("FieldAtOffset(0xB0028) = %q, want invader_alive", got)
	}
	if got := FieldAtOffset(c, "0xB0030"); got != "" { // past the array
		t.Fatalf("FieldAtOffset(0xB0030) = %q, want ''", got)
	}
}

// Verified facts come only from PASSING checks: a passing reads-postcondition
// proves the cell WRITES that field; a passing scenario's seed proves it READS.
func TestDeriveFromChecksUsesOnlyPassingChecks(t *testing.T) {
	c := testContract()
	suite := &AcceptanceSuite{Scenarios: []Scenario{
		{Name: "moves_right",
			Seed:   []SeedWrite{{At: "0x50020", U32: []uint32{68}}}, // HMI key
			Expect: ScenarioExpect{Reads: []SeedWrite{{At: "0xB0000", Cmp: "increased"}}}},
		{Name: "fires", // FAILS — must not contribute facts
			Seed:   []SeedWrite{{At: "0xB0008", U32: []uint32{0}}},
			Expect: ScenarioExpect{Reads: []SeedWrite{{At: "0xB0008", U32: []uint32{1}}}}},
	}}
	reads, writes, verified := DeriveFromChecks(suite, []bool{true, false}, c)

	if !has(writes, "player_x") {
		t.Errorf("writes = %v, want player_x (from the passing reads-assertion)", writes)
	}
	if has(writes, "bullet_active") {
		t.Error("a FAILING check must not contribute a write")
	}
	if !has(reads, "HMI input") {
		t.Errorf("reads = %v, want HMI input (seeded in the passing scenario)", reads)
	}
	if len(verified) != 1 || !strings.Contains(verified[0], "moves_right") {
		t.Fatalf("verified = %v, want only the passing check", verified)
	}
}

// The whole point of the map: a field produced by someone and consumed by nobody
// is called out as a GAP — the hint a renderer needs to stop drawing statically.
func TestRenderNamesUnconsumedFieldsAsGaps(t *testing.T) {
	m := &AppMap{
		Namespace: "urn:hdm:apps:si",
		Objective: "space invaders",
		Components: []ComponentMap{
			{Identity: "urn:hdm:apps:si:player-controller", Status: "converged", Passed: 4, Total: 4,
				Writes: []string{"player_x"}, Reads: []string{"HMI input"},
				Verified: []string{"move_right: keydown d → player_x increased"}},
			{Identity: "urn:hdm:apps:si:renderer", Entry: "render-frame", Status: "building", Passed: 1, Total: 3},
		},
	}
	out := m.Render()
	if !strings.Contains(out, "player_x: written by player-controller; read by NOBODY") {
		t.Fatalf("data flow must name the unconsumed field:\n%s", out)
	}
	if !strings.Contains(out, "GAP: player_x") {
		t.Fatalf("an unconsumed field must be flagged as a GAP:\n%s", out)
	}
	if !strings.Contains(out, "verified: move_right") {
		t.Fatalf("components must show what they are PROVEN to do:\n%s", out)
	}
}

func has(v []string, s string) bool {
	for _, x := range v {
		if x == s {
			return true
		}
	}
	return false
}

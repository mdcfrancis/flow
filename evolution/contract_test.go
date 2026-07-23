package evolution

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mdcfrancis/flow/compiler"
	"github.com/mdcfrancis/flow/storage"
)

func TestAppContractRoundTripAndNamespace(t *testing.T) {
	le, err := storage.NewLedgerEngine(filepath.Join(t.TempDir(), "hdm.db"))
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	defer le.Close()

	if LoadContract(le, "urn:hdm:apps:x") != nil {
		t.Fatal("missing contract should be nil")
	}
	c := &AppContract{Fields: []ContractField{{Name: "player_x", Offset: 0xB0000, Type: "i32", Desc: "player x"}}}
	if err := SaveContract(le, "urn:hdm:apps:x", c); err != nil {
		t.Fatalf("save: %v", err)
	}
	got := LoadContract(le, "urn:hdm:apps:x")
	if got == nil || len(got.Fields) != 1 || got.Fields[0].Offset != 0xB0000 {
		t.Fatalf("roundtrip = %+v", got)
	}
	if got.Render() == "" {
		t.Fatal("Render should be non-empty")
	}
	for in, want := range map[string]string{
		"urn:hdm:apps:space_invaders:renderer": "urn:hdm:apps:space_invaders",
		"urn:hdm:sys:optimizer":                "",
	} {
		if AppNamespaceOf(in) != want {
			t.Errorf("AppNamespaceOf(%q)=%q want %q", in, AppNamespaceOf(in), want)
		}
	}
}

// TestScenarioReadsAssertsSharedMemory verifies the coordination test path: a
// cell that writes a shared field on input, checked via expect.reads.
func TestScenarioReadsAssertsSharedMemory(t *testing.T) {
	// run-tick: if buttons bit0 (0x50008) set, write player_x=4 at 0xB0000.
	wat := `(module (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 100))
	  (func (export "run-tick") (param i32 i32) (result i32)
	    i32.const 327688 i32.load i32.const 1 i32.and
	    if i32.const 720896 i32.const 4 i32.store end
	    i32.const 0))`
	art, err := compiler.NewCompilerService().CompileGenotype(wat)
	if err != nil || !art.SyntaxPassed {
		t.Fatalf("compile: %v", err)
	}

	pressed := []Scenario{{
		Name:   "right moves player_x",
		Seed:   []SeedWrite{{At: "0x50008", U32: []uint32{1}}},
		Expect: ScenarioExpect{Reads: []SeedWrite{{At: "0xB0000", U32: []uint32{4}}}},
	}}
	if p, tot := ScenarioScore(context.Background(), art.Bytecode, pressed, DefaultPayloadOffset, DefaultStateWindow, nil); p != 1 || tot != 1 {
		t.Fatalf("pressed reads: %d/%d, want 1/1", p, tot)
	}
	// No input -> player_x stays 0 -> the reads assertion (expect 4) must fail.
	idle := []Scenario{{
		Name:   "no input",
		Expect: ScenarioExpect{Reads: []SeedWrite{{At: "0xB0000", U32: []uint32{4}}}},
	}}
	if p, _ := ScenarioScore(context.Background(), art.Bytecode, idle, DefaultPayloadOffset, DefaultStateWindow, nil); p != 0 {
		t.Fatalf("idle reads: passed %d, want 0", p)
	}
}

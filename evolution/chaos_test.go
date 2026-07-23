package evolution

import (
	"context"
	"testing"

	"github.com/mdcfrancis/flow/compiler"
)

// records a single-input corpus from the given cell with no chaos.
func recordOne(t *testing.T, bytecode []byte, payload []byte) []RegressionCase {
	t.Helper()
	c := NewCompactor("run-tick", DefaultPayloadOffset, DefaultStateWindow, nil)
	rc, keep, err := c.Consider(context.Background(), bytecode, payload, 1000, [16]byte{})
	if err != nil || !keep {
		t.Fatalf("record: keep=%v err=%v", keep, err)
	}
	return []RegressionCase{rc}
}

func TestChaosPassesRobustCell(t *testing.T) {
	// Returns a constant; ignores clock, memory, signals.
	robust := `(module ` + sharedImport + `
	  (func (export "run-tick") (param i32 i32) (result i32) i32.const 1))`
	art, _ := compiler.NewCompilerService().CompileGenotype(robust)
	cases := recordOne(t, art.Bytecode, []byte{1})

	chaos := ChaosProfile{ClockDriftNS: 1_000_000_000}
	ok, reason := SurvivesChaos(context.Background(), art.Bytecode, "run-tick", cases,
		DefaultPayloadOffset, DefaultStateWindow, chaos, nil)
	if !ok {
		t.Fatalf("robust cell failed chaos: %s", reason)
	}
}

func TestChaosRejectsEntropyDependentCell(t *testing.T) {
	// Stores a chronos entropy byte into its state region: fragile to scramble.
	fragile := `(module ` + sharedImport + `
	  (import "hdm:kernel/chronos" "entropy" (func $ent (param i32) (result i32)))
	  (func (export "run-tick") (param i32 i32) (result i32)
	    i32.const 0 i32.const 0 call $ent i32.store
	    i32.const 1))`
	art, err := compiler.NewCompilerService().CompileGenotype(fragile)
	if err != nil || !art.SyntaxPassed {
		t.Fatalf("compile: %v", err)
	}
	cases := recordOne(t, art.Bytecode, []byte{1})

	ok, reason := SurvivesChaos(context.Background(), art.Bytecode, "run-tick", cases,
		DefaultPayloadOffset, DefaultStateWindow, ChaosProfile{EntropyScramble: true}, nil)
	if ok {
		t.Fatal("entropy-dependent cell should fail entropy-scramble chaos")
	}
	t.Logf("correctly rejected: %s", reason)
}

func TestChaosRejectsFragileClockCell(t *testing.T) {
	// Writes the current clock into its state region: fragile to clock drift.
	fragile := `(module ` + sharedImport + `
	  (import "hdm:kernel/chronos" "now-ns" (func $now (result i64)))
	  (func (export "run-tick") (param i32 i32) (result i32)
	    i32.const 0 call $now i32.wrap_i64 i32.store
	    i32.const 1))`
	art, err := compiler.NewCompilerService().CompileGenotype(fragile)
	if err != nil || !art.SyntaxPassed {
		t.Fatalf("compile fragile: %v", err)
	}
	cases := recordOne(t, art.Bytecode, []byte{1})

	// Recorded at clock=1000; chaos drifts the clock, so the stored value
	// changes and the state hash diverges — the cell is not robust.
	chaos := ChaosProfile{ClockDriftNS: 1_000_000_000}
	ok, reason := SurvivesChaos(context.Background(), art.Bytecode, "run-tick", cases,
		DefaultPayloadOffset, DefaultStateWindow, chaos, nil)
	if ok {
		t.Fatal("clock-dependent cell should fail clock-drift chaos")
	}
	t.Logf("correctly rejected: %s", reason)
}

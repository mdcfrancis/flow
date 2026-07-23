package evolution

import (
	"context"
	"strings"
	"testing"
)

// A syntactically valid module whose run-tick has the WRONG arity (4 params) —
// exactly the failure seen in the wild, where such a candidate compiled and then
// trapped in the gauntlet ("expected 4 params, but passed 2").
const fourParamRunTick = `(module
  (memory (export "mem") 1)
  (func (export "run-tick") (param i32 i32 i32 i32) (result i32) i32.const 1))`

// A conforming (param i32 i32)(result i32) run-tick.
const twoParamRunTick = `(module
  (memory (export "mem") 1)
  (func (export "run-tick") (param i32 i32) (result i32) i32.const 1))`

// render-frame with the right shape but the WRONG export name for a run-tick
// contract.
const wrongName = `(module
  (memory (export "mem") 1)
  (func (export "render-frame") (param i32 i32) (result i32) i32.const 1))`

func TestSieveRepairsWrongArity(t *testing.T) {
	// First draft violates the contract (4 params); second draft conforms.
	model := &fakeReasoner{responses: []string{fourParamRunTick, twoParamRunTick}}
	out, err := RunSieve(context.Background(), model, "compass", "seed", 5, RunTickContract)
	if err != nil {
		t.Fatalf("expected convergence after repair, got %v", err)
	}
	if out.Iterations != 2 {
		t.Fatalf("converged at iteration %d, want 2 (arity rejected then repaired)", out.Iterations)
	}
	// The model must have been re-prompted with a signature correction directive.
	if model.calls != 2 {
		t.Fatalf("model invoked %d times, want 2", model.calls)
	}
}

func TestSieveRejectsPersistentArityViolation(t *testing.T) {
	// Always emits the wrong arity: the sieve must exhaust its budget and fail
	// with an entry-contract diagnostic, never returning the bad candidate.
	model := &fakeReasoner{responses: []string{fourParamRunTick}}
	_, err := RunSieve(context.Background(), model, "compass", "seed", 3, RunTickContract)
	if err == nil {
		t.Fatal("expected entry-contract failure")
	}
	if !strings.Contains(err.Error(), "entry contract") {
		t.Fatalf("error should cite the entry contract, got: %v", err)
	}
	if model.calls != 3 {
		t.Fatalf("model invoked %d times, want 3 (maxIters)", model.calls)
	}
}

func TestSieveRejectsWrongExportName(t *testing.T) {
	model := &fakeReasoner{responses: []string{wrongName}}
	_, err := RunSieve(context.Background(), model, "compass", "seed", 2, RunTickContract)
	if err == nil {
		t.Fatal("expected failure: run-tick export missing")
	}
}

func TestSieveNoContractSkipsSignatureCheck(t *testing.T) {
	// With no contract, the 4-param module clears the sieve (back-compat).
	model := &fakeReasoner{responses: []string{fourParamRunTick}}
	out, err := RunSieve(context.Background(), model, "compass", "seed", 2)
	if err != nil || out.Iterations != 1 {
		t.Fatalf("no-contract sieve should accept on iter 1, got iter=%v err=%v", out.Iterations, err)
	}
}

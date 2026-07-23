package compiler

import "testing"

func TestCountImportCalls(t *testing.T) {
	wat := `(module
	  (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 100))
	  (import "hdm:kernel/cognitive-engine" "invoke-reasoning"
	    (func $reason (param i32 i32 i32 i32) (result i32 i32)))
	  (func (export "run-tick") (param i32 i32) (result i32)
	    i32.const 0 i32.const 0 i32.const 0 i32.const 0
	    call $reason drop drop
	    i32.const 0 i32.const 0 i32.const 0 i32.const 0
	    (call $reason) drop drop
	    i32.const 1))`
	// Two calls: one linear, one folded. The memory import must not shift the
	// func index used for the numeric-call check.
	n, err := CountImportCalls(wat, "hdm:kernel/cognitive-engine", "invoke-reasoning")
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 2 {
		t.Fatalf("call count = %d, want 2", n)
	}

	// Absent import => zero.
	n, err = CountImportCalls(wat, "hdm:kernel/nope", "missing")
	if err != nil || n != 0 {
		t.Fatalf("absent import count = %d, %v; want 0, nil", n, err)
	}

	// A candidate that dropped the reasoning call.
	dropped := `(module
	  (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 100))
	  (import "hdm:kernel/cognitive-engine" "invoke-reasoning"
	    (func $reason (param i32 i32 i32 i32) (result i32 i32)))
	  (func (export "run-tick") (param i32 i32) (result i32) i32.const 1))`
	n, err = CountImportCalls(dropped, "hdm:kernel/cognitive-engine", "invoke-reasoning")
	if err != nil || n != 0 {
		t.Fatalf("dropped-call count = %d, %v; want 0, nil", n, err)
	}
}

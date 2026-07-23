package appgen

import (
	"context"
	"testing"

	"github.com/mdcfrancis/flow/compiler"
	"github.com/mdcfrancis/flow/evolution"
	"github.com/mdcfrancis/flow/execution"
)

func scoreWAT(t *testing.T, wat string, suite *evolution.AcceptanceSuite) (passed, total int) {
	t.Helper()
	art, err := compiler.NewCompilerService().CompileGenotype(wat)
	if err != nil || !art.SyntaxPassed {
		t.Fatalf("compile: %v (%s)", err, art.ErrorContext)
	}
	return evolution.ScoreSuite(context.Background(), art.Bytecode, "run-tick", suite,
		evolution.DefaultPayloadOffset, evolution.DefaultStateWindow, nil)
}

// TestListAcceptancePasses: the real sys:list fully passes; a broken list (append that doesn't
// bump the length) fails.
func TestListAcceptancePasses(t *testing.T) {
	suite := ListAcceptance(evolution.DefaultPayloadOffset)
	if p, total := scoreWAT(t, execution.SysListWAT, suite); total == 0 || p != total {
		t.Fatalf("real sys:list must fully pass, got %d/%d", p, total)
	}
	// Broken: append writes the element but never increments the length header.
	broken := `(module
  (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 100))
  (func (export "run-tick") (param $arg i32) (param $len i32) (result i32)
    (local $op i32) (local $x i32) (local $n i32)
    (local.set $op (i32.load offset=0 (local.get $arg)))
    (local.set $x  (i32.load offset=4 (local.get $arg)))
    (local.set $n  (i32.load (i32.const 0x00400000)))
    (if (i32.eq (local.get $op) (i32.const 1))
      (then
        (i32.store (i32.add (i32.const 0x00400004) (i32.mul (local.get $n) (i32.const 4))) (local.get $x))
        (return (local.get $n)))) ;; BUG: no length bump, wrong return
    (if (i32.eq (local.get $op) (i32.const 2))
      (then (return (i32.load (i32.add (i32.const 0x00400004) (i32.mul (local.get $x) (i32.const 4)))))))
    (local.get $n)))`
	if p, total := scoreWAT(t, broken, suite); p == total {
		t.Fatalf("a list whose append drops the length bump must FAIL, got %d/%d", p, total)
	}
}

// TestSetAcceptancePasses: the real sys:set fully passes; a direct-mapped (no-probe) set fails
// the collision cases.
func TestSetAcceptancePasses(t *testing.T) {
	suite := SetAcceptance(evolution.DefaultPayloadOffset)
	if p, total := scoreWAT(t, execution.SysSetWAT, suite); total == 0 || p != total {
		t.Fatalf("real sys:set must fully pass, got %d/%d", p, total)
	}
	directMapped := `(module
  (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 100))
  (func (export "run-tick") (param $arg i32) (param $len i32) (result i32)
    (local $op i32) (local $key i32) (local $addr i32)
    (local.set $op  (i32.load offset=0 (local.get $arg)))
    (local.set $key (i32.load offset=4 (local.get $arg)))
    (local.set $addr (i32.add (i32.const 0x00400000) (i32.mul (i32.rem_u (local.get $key) (i32.const 512)) (i32.const 4))))
    (if (i32.eq (local.get $op) (i32.const 1))
      (then (i32.store (local.get $addr) (local.get $key)) (return (i32.const 1))))
    (if (i32.eq (i32.load (local.get $addr)) (local.get $key)) (then (return (i32.const 1))))
    (i32.const 0)))`
	if p, total := scoreWAT(t, directMapped, suite); p == total {
		t.Fatalf("a direct-mapped (no-probe) set must FAIL the collision cases, got %d/%d", p, total)
	}
}

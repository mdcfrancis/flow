package appgen

import (
	"context"
	"testing"

	"github.com/mdcfrancis/flow/compiler"
	"github.com/mdcfrancis/flow/evolution"
	"github.com/mdcfrancis/flow/execution"
)

// TestDictAcceptancePasses: the real sys:dict cell fully passes its acceptance (get/insert,
// overwrite, and linear-probe collisions).
func TestDictAcceptancePasses(t *testing.T) {
	cs := compiler.NewCompilerService()
	art, err := cs.CompileGenotype(execution.SysDictWAT)
	if err != nil || !art.SyntaxPassed {
		t.Fatalf("compile sys:dict: %v (%s)", err, art.ErrorContext)
	}
	suite := DictAcceptance(evolution.DefaultPayloadOffset)
	passed, total := evolution.ScoreSuite(context.Background(), art.Bytecode, "run-tick", suite,
		evolution.DefaultPayloadOffset, evolution.DefaultStateWindow, nil)
	if total == 0 || passed != total {
		t.Fatalf("real sys:dict must fully pass its acceptance, got %d/%d", passed, total)
	}
}

// TestBrokenDictFails confirms the suite pins behavior: a dict that ignores the probe (direct
// mapping — home slot only) fails the collision cases, and an always-0 get fails the hits.
func TestBrokenDictFails(t *testing.T) {
	cs := compiler.NewCompilerService()
	suite := DictAcceptance(evolution.DefaultPayloadOffset)

	// A direct-mapped dict: no probing — get/insert only ever touch the home slot. Passes the
	// non-colliding cases but MUST fail the collision ones.
	directMapped := `(module
  (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 100))
  (func (export "run-tick") (param $arg i32) (param $len i32) (result i32)
    (local $op i32) (local $key i32) (local $val i32) (local $addr i32)
    (local.set $op  (i32.load offset=0 (local.get $arg)))
    (local.set $key (i32.load offset=4 (local.get $arg)))
    (local.set $val (i32.load offset=8 (local.get $arg)))
    (local.set $addr (i32.add (i32.const 0x00400000) (i32.mul (i32.rem_u (local.get $key) (i32.const 512)) (i32.const 8))))
    (if (i32.eq (local.get $op) (i32.const 1))
      (then
        (i32.store offset=0 (local.get $addr) (local.get $key))
        (i32.store offset=4 (local.get $addr) (local.get $val))
        (return (i32.const 1))))
    (if (i32.eq (i32.load offset=0 (local.get $addr)) (local.get $key))
      (then (return (i32.load offset=4 (local.get $addr)))))
    (i32.const 0)))`
	art, err := cs.CompileGenotype(directMapped)
	if err != nil || !art.SyntaxPassed {
		t.Fatalf("compile direct-mapped: %v (%s)", err, art.ErrorContext)
	}
	p, total := evolution.ScoreSuite(context.Background(), art.Bytecode, "run-tick", suite,
		evolution.DefaultPayloadOffset, evolution.DefaultStateWindow, nil)
	if p == total {
		t.Fatalf("a direct-mapped (no-probe) dict must FAIL the collision cases, got %d/%d", p, total)
	}

	// An always-0 dict fails everything with a nonzero expectation.
	nullDict := `(module
  (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 100))
  (func (export "run-tick") (param i32 i32) (result i32) (i32.const 0)))`
	nart, _ := cs.CompileGenotype(nullDict)
	np, nt := evolution.ScoreSuite(context.Background(), nart.Bytecode, "run-tick", suite,
		evolution.DefaultPayloadOffset, evolution.DefaultStateWindow, nil)
	if np == nt {
		t.Fatalf("a null dict must FAIL the acceptance, got %d/%d", np, nt)
	}
}

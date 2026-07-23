package appgen

import (
	"context"
	"encoding/binary"
	"testing"

	"github.com/mdcfrancis/flow/compiler"
	"github.com/mdcfrancis/flow/evolution"
)

// A hand-written sys:queue (ring buffer) matching the published ABI: [op,val] op1=enqueue->size,
// op2=dequeue->oldest. State in its private window (0x00400000: head,tail,count,slots).
const testSysQueueWAT = `(module
  (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 100))
  (func (export "run-tick") (param $arg i32) (param $len i32) (result i32)
    (local $op i32) (local $val i32) (local $head i32) (local $tail i32) (local $count i32) (local $r i32)
    (local.set $op  (i32.load offset=0 (local.get $arg)))
    (local.set $val (i32.load offset=4 (local.get $arg)))
    (local.set $head (i32.load (i32.const 0x00400000)))
    (local.set $tail (i32.load (i32.const 0x00400004)))
    (local.set $count (i32.load (i32.const 0x00400008)))
    (if (i32.eq (local.get $op) (i32.const 1))
      (then
        (i32.store (i32.add (i32.const 0x0040000C) (i32.mul (local.get $tail) (i32.const 4))) (local.get $val))
        (i32.store (i32.const 0x00400004) (i32.and (i32.add (local.get $tail) (i32.const 1)) (i32.const 15)))
        (i32.store (i32.const 0x00400008) (i32.add (local.get $count) (i32.const 1)))
        (return (i32.add (local.get $count) (i32.const 1))))
      (else
        (if (i32.eqz (local.get $count)) (then (return (i32.const 0))))
        (local.set $r (i32.load (i32.add (i32.const 0x0040000C) (i32.mul (local.get $head) (i32.const 4)))))
        (i32.store (i32.const 0x00400000) (i32.and (i32.add (local.get $head) (i32.const 1)) (i32.const 15)))
        (i32.store (i32.const 0x00400008) (i32.sub (local.get $count) (i32.const 1)))
        (return (local.get $r))))
    (i32.const 0)))`

// demo:fifo refactored to OFFLOAD its FIFO to sys:queue — the same behavior, buffer held by the
// shared primitive. This is what the model SHOULD produce.
const testFifoOffloadWAT = `(module
  (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 100))
  (import "hdm:kernel/cell-dispatch" "invoke-cell" (func $invoke (param i32 i32 i32 i32) (result i32)))
  (data (i32.const 0x00020000) "urn:hdm:sys:queue")
  (func (export "run-tick") (param $p i32) (param $len i32) (result i32)
    (local $nops i32) (local $k i32) (local $op i32) (local $arg i32) (local $acc i32) (local $val i32)
    (local.set $nops (i32.load (i32.const 0x00010000)))
    (local.set $acc (i32.const 0)) (local.set $k (i32.const 0))
    (block $done (loop $loop
      (br_if $done (i32.ge_u (local.get $k) (local.get $nops)))
      (local.set $op  (i32.load (i32.add (i32.const 0x00010004) (i32.mul (local.get $k) (i32.const 8)))))
      (local.set $arg (i32.load (i32.add (i32.const 0x00010008) (i32.mul (local.get $k) (i32.const 8)))))
      (i32.store (i32.const 0x00030000) (local.get $op))
      (i32.store (i32.const 0x00030004) (local.get $arg))
      (local.set $val (call $invoke (i32.const 0x00020000) (i32.const 17) (i32.const 0x00030000) (i32.const 8)))
      (if (i32.eq (local.get $op) (i32.const 2))
        (then (local.set $acc (i32.add (i32.mul (local.get $acc) (i32.const 2)) (local.get $val)))))
      (local.set $k (i32.add (local.get $k) (i32.const 1)))
      (br $loop)))
    (local.get $acc)))`

func u32bytes(vs ...uint32) []byte {
	b := make([]byte, 4*len(vs))
	for i, v := range vs {
		binary.LittleEndian.PutUint32(b[i*4:], v)
	}
	return b
}

// TestQueueOffloadTooSimpleToShare documents the empirical finding: a SIMPLE queue is not worth
// sharing. The offloaded demo:fifo reproduces behavior exactly, but its dispatch machinery
// (invoke import + URN data segment + arg marshalling) is LARGER than the tiny inline ring
// buffer it replaces — so under the (correct) code-size cost, offloading LOSES. Sharing pays off
// only for COMPLEX structures where the inline form far exceeds the dispatch overhead; a queue is
// below that bar, and the cost model rightly prefers inlining. (This is why the live model run
// produced no queue — the cost model correctly declines it, independent of model quality.)
func TestQueueOffloadTooSimpleToShare(t *testing.T) {
	cs := compiler.NewCompilerService()
	comp := func(w string) []byte {
		art, err := cs.CompileGenotype(w)
		if err != nil || !art.SyntaxPassed {
			t.Fatalf("compile: %v (%s)", err, art.ErrorContext)
		}
		return art.Bytecode
	}
	inline := comp(FifoWAT)
	offload := comp(testFifoOffloadWAT)
	queue := comp(testSysQueueWAT)
	resolver := func(urn string) ([]byte, bool) {
		if urn == "urn:hdm:sys:queue" {
			return queue, true
		}
		return nil, false
	}
	inputs := [][]byte{u32bytes(6, 1, 5, 1, 3, 2, 0, 1, 7, 2, 0, 2, 0)}

	v, err := evolution.RunTrajectoryGauntlet(context.Background(), inline, offload, "run-tick", inputs,
		1, 1.0, 0.0, evolution.DefaultPayloadOffset, evolution.DefaultStateWindow, resolver)
	if err != nil {
		t.Fatalf("gauntlet: %v", err)
	}
	// Behavior is preserved (the offload is a correct refactor)...
	if !v.OutputMatch {
		t.Fatalf("offloaded fifo must reproduce behavior: %s", v.Reason)
	}
	// ...but it is NOT accepted, because a simple queue offloaded is LARGER than inline.
	if v.Accepted {
		t.Fatalf("a simple queue should NOT be worth sharing (offload larger than inline); got Accepted")
	}
	if len(offload) <= len(inline) {
		t.Fatalf("expected offloaded (%d) to be larger than inline (%d) for a simple queue", len(offload), len(inline))
	}
	t.Logf("simple queue is not worth sharing: inline=%d bytes, offloaded=%d bytes (+queue %d) — cost model correctly declines", len(inline), len(offload), len(queue))
}

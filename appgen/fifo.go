package appgen

import "github.com/mdcfrancis/flow/evolution"

// demo:fifo is the two-changes queue probe. In ONE tick it processes an interleaved sequence of
// enqueue/dequeue operations from the payload, maintaining a FIFO, and folds each dequeued value
// into an ORDER-DEPENDENT accumulator (acc = acc*2 + dequeued) — so the result pins FIFO order,
// checkable by a single-tick acceptance (no stateful multi-op scenario needed).
//
// The baseline holds the FIFO inline (ring buffer + head/tail in its window). A refactor that
// OFFLOADS the FIFO to a shared sys:queue primitive shrinks the cell a lot, and the sys:queue
// dispatches are refunded (free shared infrastructure) — so with the code-size term + the
// sys-dispatch refund, offloading is a net win and the system should MINT sys:queue and use it.
//
// Payload at 0x10000: [nops, op0, arg0, op1, arg1, ...] (op 1=enqueue(arg), 2=dequeue).

const FifoURN = "urn:hdm:demo:fifo"

const FifoWAT = `(module
  (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 100))
  (func (export "run-tick") (param $p i32) (param $len i32) (result i32)
    (local $nops i32) (local $k i32) (local $op i32) (local $arg i32)
    (local $head i32) (local $tail i32) (local $acc i32) (local $val i32)
    (local.set $nops (i32.load (i32.const 0x00010000)))
    (local.set $head (i32.const 0)) (local.set $tail (i32.const 0)) (local.set $acc (i32.const 0))
    (local.set $k (i32.const 0))
    (block $done (loop $loop
      (br_if $done (i32.ge_u (local.get $k) (local.get $nops)))
      (local.set $op  (i32.load (i32.add (i32.const 0x00010004) (i32.mul (local.get $k) (i32.const 8)))))
      (local.set $arg (i32.load (i32.add (i32.const 0x00010008) (i32.mul (local.get $k) (i32.const 8)))))
      (if (i32.eq (local.get $op) (i32.const 1))
        (then
          (i32.store (i32.add (i32.const 0x00400040) (i32.mul (local.get $tail) (i32.const 4))) (local.get $arg))
          (local.set $tail (i32.and (i32.add (local.get $tail) (i32.const 1)) (i32.const 15))))
        (else
          (local.set $val (i32.load (i32.add (i32.const 0x00400040) (i32.mul (local.get $head) (i32.const 4)))))
          (local.set $head (i32.and (i32.add (local.get $head) (i32.const 1)) (i32.const 15)))
          (local.set $acc (i32.add (i32.mul (local.get $acc) (i32.const 2)) (local.get $val)))))
      (local.set $k (i32.add (local.get $k) (i32.const 1)))
      (br $loop)))
    (local.get $acc)))`

// SysQueueGuidance describes the desired shared sys:queue ABI — the human supplies the interface
// (the "we need a FIFO queue" goal), the system implements + integrates it. Published as system
// guidance so a cell refactoring demo:fifo mints sys:queue to that ABI.
const SysQueueGuidance = "A shared FIFO QUEUE primitive urn:hdm:sys:queue would serve cells that maintain enqueue/dequeue order. " +
	"Op ABI (opcode-in-args, over its private window): [op,val] op1=ENQUEUE(val)->size, op2=DEQUEUE->oldest value (0 if empty). " +
	"A cell doing FIFO work should dispatch to urn:hdm:sys:queue via invoke-cell (mint it if it does not exist yet) instead of holding the buffer inline."

// FifoAcceptance pins demo:fifo's FIFO behavior (order-dependent accumulator) via single-tick
// scenarios. Values computed by hand from acc = acc*2 + dequeued in FIFO order.
func FifoAcceptance(p uint32) *evolution.AcceptanceSuite {
	mk := func(name string, words []uint32, want int32) evolution.Scenario {
		return evolution.Scenario{
			Name: name, Entry: "run-tick", Steps: 1,
			Seed:   []evolution.SeedWrite{{At: at(p), U32: words}},
			Expect: evolution.ScenarioExpect{Result: i32p(want)},
		}
	}
	return &evolution.AcceptanceSuite{Scenarios: []evolution.Scenario{
		// enq5 enq3 deq(5) enq7 deq(3) deq(7): 5, 13, 33.
		mk("fifo_a", []uint32{6, 1, 5, 1, 3, 2, 0, 1, 7, 2, 0, 2, 0}, 33),
		// enq10 deq(10) enq20 deq(20): 10, 40.
		mk("fifo_b", []uint32{4, 1, 10, 2, 0, 1, 20, 2, 0}, 40),
		// enq1 enq2 enq3 deq deq deq: 1, 4, 11.
		mk("fifo_c", []uint32{6, 1, 1, 1, 2, 1, 3, 2, 0, 2, 0, 2, 0}, 11),
	}}
}

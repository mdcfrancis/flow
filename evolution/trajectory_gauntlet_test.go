package evolution

import (
	"context"
	"encoding/binary"
	"testing"

	"github.com/mdcfrancis/flow/compiler"
)

// Fuel is metered per function-boundary crossing, so cost differences must show up as CALLS.
// $step is a helper invoked once per loop iteration — sum(1..N) costs ~N calls per tick.

// sumN: recompute via N helper calls every tick — the expensive baseline.
const sumNWAT = `(module
  (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 100))
  (func $step (param $a i32) (param $b i32) (result i32) (i32.add (local.get $a) (local.get $b)))
  (func (export "run-tick") (param $p i32) (param $len i32) (result i32)
    (local $n i32) (local $i i32) (local $acc i32)
    (local.set $n (i32.load (local.get $p)))
    (local.set $i (i32.const 1)) (local.set $acc (i32.const 0))
    (block $done (loop $loop
      (br_if $done (i32.gt_u (local.get $i) (local.get $n)))
      (local.set $acc (call $step (local.get $acc) (local.get $i)))
      (local.set $i (i32.add (local.get $i) (i32.const 1)))
      (br $loop)))
    (local.get $acc)))`

// sumNCached: same output, but caches (key=N+1, val=sum) in a private page (0x00400000). On a
// repeat of the same N it returns the cached value with ZERO helper calls — an AMORTIZED win
// only visible when memory persists across calls (a trajectory), not per isolated case.
const sumNCachedWAT = `(module
  (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 100))
  (func $step (param $a i32) (param $b i32) (result i32) (i32.add (local.get $a) (local.get $b)))
  (func (export "run-tick") (param $p i32) (param $len i32) (result i32)
    (local $n i32) (local $i i32) (local $acc i32)
    (local.set $n (i32.load (local.get $p)))
    (if (i32.eq (i32.load (i32.const 0x00400000)) (i32.add (local.get $n) (i32.const 1)))
      (then (return (i32.load (i32.const 0x00400004)))))
    (local.set $i (i32.const 1)) (local.set $acc (i32.const 0))
    (block $done (loop $loop
      (br_if $done (i32.gt_u (local.get $i) (local.get $n)))
      (local.set $acc (call $step (local.get $acc) (local.get $i)))
      (local.set $i (i32.add (local.get $i) (i32.const 1)))
      (br $loop)))
    (i32.store (i32.const 0x00400000) (i32.add (local.get $n) (i32.const 1)))
    (i32.store (i32.const 0x00400004) (local.get $acc))
    (local.get $acc)))`

func leU32(n uint32) []byte {
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, n)
	return b
}

func compileTraj(t *testing.T, cs *compiler.CompilerService, wat string) []byte {
	t.Helper()
	art, err := cs.CompileGenotype(wat)
	if err != nil || !art.SyntaxPassed {
		t.Fatalf("compile: %v (%s)", err, art.ErrorContext)
	}
	return art.Bytecode
}

// TestTrajectoryGauntletRewardsAmortization: a caching refactor beats the recomputing baseline
// over a repeated-input trajectory (behavior identical, total fuel lower), while an identical
// candidate is correctly refused.
func TestTrajectoryGauntletRewardsAmortization(t *testing.T) {
	cs := compiler.NewCompilerService()
	base := compileTraj(t, cs, sumNWAT)
	cached := compileTraj(t, cs, sumNCachedWAT)
	ctx := context.Background()

	// Consecutive repeats so a single-entry cache hits: sum(50) x3, sum(30) x3.
	traj := [][]byte{leU32(50), leU32(50), leU32(50), leU32(30), leU32(30), leU32(30)}

	tv, err := RunTrajectoryGauntlet(ctx, base, cached, EntryPoint, traj, 1, 1.0, 0.0,
		DefaultPayloadOffset, DefaultStateWindow, nil)
	if err != nil {
		t.Fatalf("trajectory gauntlet: %v", err)
	}
	if !tv.OutputMatch {
		t.Fatalf("cached candidate must reproduce outputs: %s", tv.Reason)
	}
	if !tv.Accepted {
		t.Fatalf("amortized win must be accepted: %s (fuel %d vs %d)", tv.Reason, tv.CandidateFuel, tv.BaselineFuel)
	}
	if tv.CandidateFuel >= tv.BaselineFuel {
		t.Fatalf("cached candidate should spend less total fuel: %d vs %d", tv.CandidateFuel, tv.BaselineFuel)
	}
	t.Logf("amortized fuel %d -> %d over %d steps", tv.BaselineFuel, tv.CandidateFuel, len(traj))

	// An identical candidate has no amortized win -> refused.
	nv, err := RunTrajectoryGauntlet(ctx, base, base, EntryPoint, traj, 1, 1.0, 0.0,
		DefaultPayloadOffset, DefaultStateWindow, nil)
	if err != nil {
		t.Fatalf("trajectory gauntlet (identical): %v", err)
	}
	if nv.Accepted {
		t.Fatalf("an identical candidate must NOT be accepted (no energy reduction)")
	}
}

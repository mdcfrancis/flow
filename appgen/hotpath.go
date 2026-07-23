package appgen

import "github.com/mdcfrancis/flow/evolution"

// demo:hotpath + demo:hotleaf are a crafted EXPENSIVE + CACHEABLE workload for proving the
// structural optimizer's AMORTIZED (trajectory) path end to end. hotpath reads K from the
// payload (0x10000) and returns sum(1..K) — computed by DISPATCHING to demo:hotleaf K times
// (each echoes its argument). Three properties make it the pure probe:
//   - EXPENSIVE: fuel meters function-boundary crossings, and K dispatches make ~2K of them.
//   - NON-INLINABLE: the cost is CROSS-CELL dispatch (invoke-cell), which the model cannot
//     inline or unroll away (unlike a helper or recursion) — so a per-case win is impossible.
//     The ONLY way to lower cost is to CACHE the result, and a cache only pays off across
//     repeated inputs — so a commit MUST come through the amortized TRAJECTORY gauntlet.
//   - CACHEABLE: sum(1..K) is deterministic in K, so a memo makes a repeat free.
// K is masked to 0..31 so a random corpus probe cannot spin unbounded dispatch.

const HotpathURN = "urn:hdm:demo:hotpath"
const HotleafURN = "urn:hdm:demo:hotleaf"

// HotleafWAT: echoes the i32 at its argument pointer. hotpath dispatches to it K times.
const HotleafWAT = `(module
  (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 100))
  (func (export "run-tick") (param $p i32) (param $len i32) (result i32)
    (i32.load (local.get $p))))`

// HotpathWAT: sum(1..K) via K dispatches to demo:hotleaf. The leaf URN is embedded in a data
// segment at 0x20000; the per-call argument (the running index) is written at 0x30000.
const HotpathWAT = `(module
  (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 100))
  (import "hdm:kernel/cell-dispatch" "invoke-cell" (func $invoke (param i32 i32 i32 i32) (result i32)))
  (data (i32.const 0x00020000) "urn:hdm:demo:hotleaf")
  (func (export "run-tick") (param $p i32) (param $len i32) (result i32)
    (local $k i32) (local $i i32) (local $acc i32)
    (local.set $k (i32.and (i32.load (i32.const 0x00010000)) (i32.const 31)))
    (local.set $i (i32.const 1)) (local.set $acc (i32.const 0))
    (block $done (loop $loop
      (br_if $done (i32.gt_u (local.get $i) (local.get $k)))
      (i32.store (i32.const 0x00030000) (local.get $i))
      (local.set $acc (i32.add (local.get $acc)
        (call $invoke (i32.const 0x00020000) (i32.const 20) (i32.const 0x00030000) (i32.const 4))))
      (local.set $i (i32.add (local.get $i) (i32.const 1)))
      (br $loop)))
    (local.get $acc)))`

// HotpathAcceptance pins demo:hotpath's behavior: result = sum(1..K) = K(K+1)/2. Payload at p:
// [K]. Scoring must supply a resolver that resolves demo:hotleaf so the dispatch runs.
func HotpathAcceptance(p uint32) *evolution.AcceptanceSuite {
	mk := func(name string, k, want uint32) evolution.Scenario {
		return evolution.Scenario{
			Name: name, Entry: "run-tick", Steps: 1,
			Seed:   []evolution.SeedWrite{{At: at(p), U32: []uint32{k}}},
			Expect: evolution.ScenarioExpect{Result: i32p(int32(want))},
		}
	}
	return &evolution.AcceptanceSuite{Scenarios: []evolution.Scenario{
		mk("hot_k3", 3, 6),
		mk("hot_k5", 5, 15),
		mk("hot_k7", 7, 28),
		mk("hot_k4", 4, 10),
	}}
}

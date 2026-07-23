package appgen

import (
	"encoding/binary"
	"fmt"

	"github.com/mdcfrancis/flow/evolution"
	"github.com/mdcfrancis/flow/execution"
)

// Activating the WAT substrate: the sys:* combinators become EVOLVABLE cells. Enrolled in the
// annealing loop with a behavior-pinning ACCEPTANCE suite, evolution drives them toward lower
// fuel (a fused/faster map) while a mutation that changes their behavior is rejected. The
// winners are epoch-frozen. The acceptance dispatches a tiny test LEAF, which the shadow
// sandbox resolves via the orchestrator's resolver (real inter-cell dispatch in verification).

// EvolvableLeaf is a tiny pure function cell used ONLY to verify a combinator during evolution.
type EvolvableLeaf struct{ URN, WAT string }

const (
	leafDoubleURN  = "urn:hdm:sys:test-double"  // map: x -> 2x
	leafSumPairURN = "urn:hdm:sys:test-sumpair" // fold/scan/zip: [a,b] -> a+b
	leafPosURN     = "urn:hdm:sys:test-pos"     // filter: x -> x>0
	leafDecURN     = "urn:hdm:sys:test-dec"     // iterate: state-- ; return state>0
)

// CombinatorTestLeaves are the fixture cells the combinator acceptance dispatches. They must be
// seeded so the resolver can find them during scoring.
func CombinatorTestLeaves() []EvolvableLeaf {
	return []EvolvableLeaf{
		{leafDoubleURN, `(module
  (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 1))
  (func (export "run-tick") (param i32 i32) (result i32)
    (i32.mul (i32.const 2) (i32.load (local.get 0)))))`},
		{leafSumPairURN, `(module
  (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 1))
  (func (export "run-tick") (param i32 i32) (result i32)
    (i32.add (i32.load (local.get 0)) (i32.load offset=4 (local.get 0)))))`},
		{leafPosURN, `(module
  (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 1))
  (func (export "run-tick") (param i32 i32) (result i32)
    (i32.gt_s (i32.load (local.get 0)) (i32.const 0))))`},
		{leafDecURN, `(module
  (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 1))
  (func (export "run-tick") (param i32 i32) (result i32)
    (i32.store (local.get 0) (i32.sub (i32.load (local.get 0)) (i32.const 1)))
    (i32.gt_s (i32.load (local.get 0)) (i32.const 0))))`},
	}
}

func i32p(v int32) *int32 { return &v }
func u(v int32) uint32    { return uint32(v) }

func packWords(b []byte) []uint32 {
	for len(b)%4 != 0 {
		b = append(b, 0)
	}
	w := make([]uint32, len(b)/4)
	for i := range w {
		w[i] = binary.LittleEndian.Uint32(b[i*4:])
	}
	return w
}

func at(off uint32) string { return fmt.Sprintf("0x%X", off) }

// mapCase builds one map-acceptance scenario: seed the leaf URN + config + input at the payload
// region, run map's run-tick, and assert the output array equals want.
func mapCase(name string, p uint32, leafURN string, in []uint32, elemWords uint32, want []uint32) evolution.Scenario {
	const urnOff, inOff, outOff = 0x100, 0x200, 0x300
	n := uint32(len(want))
	return evolution.Scenario{
		Name: name, Entry: "run-tick", Steps: 1,
		Seed: []evolution.SeedWrite{
			// config {fnPtr, fnLen, inPtr, outPtr, n, elemWords} at the arg pointer (p).
			{At: at(p), U32: []uint32{p + urnOff, uint32(len(leafURN)), p + inOff, p + outOff, n, elemWords}},
			{At: at(p + urnOff), U32: packWords([]byte(leafURN))},
			{At: at(p + inOff), U32: in},
		},
		Expect: evolution.ScenarioExpect{Reads: []evolution.SeedWrite{{At: at(p + outOff), U32: want}}},
	}
}

// MapAcceptance is the behavior contract for sys:map — several (leaf, input → output) cases
// with different leaves, lengths, and strides, so a mutation cannot game one case while
// breaking general map. This is the fitness anchor that lets map evolve safely.
func MapAcceptance(payloadOffset uint32) *evolution.AcceptanceSuite {
	p := payloadOffset
	return &evolution.AcceptanceSuite{Scenarios: []evolution.Scenario{
		mapCase("map_double_1234", p, leafDoubleURN, []uint32{1, 2, 3, 4}, 1, []uint32{2, 4, 6, 8}),
		mapCase("map_double_3", p, leafDoubleURN, []uint32{5, 10, 15}, 1, []uint32{10, 20, 30}),
		mapCase("map_sumpair_stride2", p, leafSumPairURN, []uint32{1, 2, 3, 4, 5, 6}, 2, []uint32{3, 7, 11}),
	}}
}

// foldCase: config {fn,inPtr,n,elemWords,seed,argBuf}; returns the accumulator.
func foldCase(name string, p uint32, leaf string, in []uint32, ew, seed uint32, wantAcc int32) evolution.Scenario {
	const urnOff, inOff, argOff = 0x100, 0x200, 0x400
	n := uint32(len(in)) / ew
	return evolution.Scenario{Name: name, Entry: "run-tick", Steps: 1,
		Seed: []evolution.SeedWrite{
			{At: at(p), U32: []uint32{p + urnOff, uint32(len(leaf)), p + inOff, n, ew, seed, p + argOff}},
			{At: at(p + urnOff), U32: packWords([]byte(leaf))},
			{At: at(p + inOff), U32: in},
		},
		Expect: evolution.ScenarioExpect{Result: i32p(wantAcc)}}
}

// filterCase: config {fn,inPtr,outPtr,n,elemWords}; returns kept count, writes kept to out.
func filterCase(name string, p uint32, leaf string, in []uint32, ew uint32, wantOut []uint32) evolution.Scenario {
	const urnOff, inOff, outOff = 0x100, 0x200, 0x300
	n := uint32(len(in)) / ew
	return evolution.Scenario{Name: name, Entry: "run-tick", Steps: 1,
		Seed: []evolution.SeedWrite{
			{At: at(p), U32: []uint32{p + urnOff, uint32(len(leaf)), p + inOff, p + outOff, n, ew}},
			{At: at(p + urnOff), U32: packWords([]byte(leaf))},
			{At: at(p + inOff), U32: in},
		},
		Expect: evolution.ScenarioExpect{Result: i32p(int32(len(wantOut))), Reads: []evolution.SeedWrite{{At: at(p + outOff), U32: wantOut}}}}
}

// scanCase: config {fn,inPtr,outPtr,n,elemWords,seed,argBuf}; writes the running accumulator.
func scanCase(name string, p uint32, leaf string, in []uint32, ew, seed uint32, wantOut []uint32) evolution.Scenario {
	const urnOff, inOff, outOff, argOff = 0x100, 0x200, 0x300, 0x400
	n := uint32(len(in)) / ew
	return evolution.Scenario{Name: name, Entry: "run-tick", Steps: 1,
		Seed: []evolution.SeedWrite{
			{At: at(p), U32: []uint32{p + urnOff, uint32(len(leaf)), p + inOff, p + outOff, n, ew, seed, p + argOff}},
			{At: at(p + urnOff), U32: packWords([]byte(leaf))},
			{At: at(p + inOff), U32: in},
		},
		Expect: evolution.ScenarioExpect{Reads: []evolution.SeedWrite{{At: at(p + outOff), U32: wantOut}}}}
}

// zipCase: config {fn,aPtr,bPtr,outPtr,n,elemWords,argBuf}; out[i]=fn(a[i],b[i]).
func zipCase(name string, p uint32, leaf string, a, b, wantOut []uint32) evolution.Scenario {
	const urnOff, aOff, bOff, outOff, argOff = 0x100, 0x200, 0x280, 0x300, 0x400
	n := uint32(len(a))
	return evolution.Scenario{Name: name, Entry: "run-tick", Steps: 1,
		Seed: []evolution.SeedWrite{
			{At: at(p), U32: []uint32{p + urnOff, uint32(len(leaf)), p + aOff, p + bOff, p + outOff, n, 1, p + argOff}},
			{At: at(p + urnOff), U32: packWords([]byte(leaf))},
			{At: at(p + aOff), U32: a},
			{At: at(p + bOff), U32: b},
		},
		Expect: evolution.ScenarioExpect{Result: i32p(int32(n)), Reads: []evolution.SeedWrite{{At: at(p + outOff), U32: wantOut}}}}
}

// iterateCase: config {fn,statePtr,stateWords,maxSteps}; returns steps taken.
func iterateCase(name string, p uint32, leaf string, initState, stateWords, maxSteps uint32, wantSteps int32) evolution.Scenario {
	const urnOff, stOff = 0x100, 0x200
	return evolution.Scenario{Name: name, Entry: "run-tick", Steps: 1,
		Seed: []evolution.SeedWrite{
			{At: at(p), U32: []uint32{p + urnOff, uint32(len(leaf)), p + stOff, stateWords, maxSteps}},
			{At: at(p + urnOff), U32: packWords([]byte(leaf))},
			{At: at(p + stOff), U32: []uint32{initState}},
		},
		Expect: evolution.ScenarioExpect{Result: i32p(wantSteps), Reads: []evolution.SeedWrite{{At: at(p + stOff), U32: []uint32{0}}}}}
}

// EvolvableCombinators returns every combinator to enrol in evolution paired with its
// behavior-pinning acceptance — each dispatching a fixture leaf through the sandbox resolver.
func EvolvableCombinators(p uint32) map[string]*evolution.AcceptanceSuite {
	return map[string]*evolution.AcceptanceSuite{
		execution.SysMapURN: MapAcceptance(p),
		execution.SysFoldURN: {Scenarios: []evolution.Scenario{
			foldCase("fold_sum", p, leafSumPairURN, []uint32{1, 2, 3, 4}, 1, 0, 10),
			foldCase("fold_seeded", p, leafSumPairURN, []uint32{10, 20, 30}, 1, 5, 65),
		}},
		execution.SysFilterURN: {Scenarios: []evolution.Scenario{
			filterCase("filter_pos", p, leafPosURN, []uint32{u(-1), 2, u(-3), 4}, 1, []uint32{2, 4}),
			filterCase("filter_pos2", p, leafPosURN, []uint32{7, u(-2), 9}, 1, []uint32{7, 9}),
		}},
		execution.SysScanURN: {Scenarios: []evolution.Scenario{
			scanCase("scan_prefix", p, leafSumPairURN, []uint32{1, 2, 3, 4}, 1, 0, []uint32{1, 3, 6, 10}),
		}},
		execution.SysZipURN: {Scenarios: []evolution.Scenario{
			zipCase("zip_add", p, leafSumPairURN, []uint32{1, 2, 3}, []uint32{10, 20, 30}, []uint32{11, 22, 33}),
		}},
		execution.SysIterateURN: {Scenarios: []evolution.Scenario{
			iterateCase("iterate_dec5", p, leafDecURN, 5, 1, 100, 5),
			iterateCase("iterate_dec3", p, leafDecURN, 3, 1, 100, 3),
		}},
	}
}

package appgen

import (
	"github.com/mdcfrancis/flow/evolution"
	"github.com/mdcfrancis/flow/execution"
)

// DictAcceptance is the behavior contract for sys:dict — single get/insert ops against a
// SEEDED window state, checking the return (get) or the resulting slot (insert). It pins the
// semantics an oracle (a Go map) would give, including linear-probe COLLISION handling, so a
// mutation that regresses to direct-mapping or loses probes fails. No dispatch/resolver is
// needed: the dict operates on its window (0x00400000) directly, which persists within a run.
//
// Op buffer at the payload offset p: [op, key, val] (op 0=GET, 1=INSERT). The dict's window
// slot for key k is DictWindowBase + (k % DictCap)*8 = {key, val}. Key 0 marks an empty slot.
func DictAcceptance(p uint32) *evolution.AcceptanceSuite {
	slot := func(k uint32) uint32 { return execution.DictWindowBase + (k%execution.DictCap)*8 }
	const GET, INS = 0, 1
	return &evolution.AcceptanceSuite{Scenarios: []evolution.Scenario{
		// get hit: slot for key 5 pre-seeded {5,111}; get(5) -> 111.
		dictCase("dict_get_hit", p,
			[]evolution.SeedWrite{{At: at(slot(5)), U32: []uint32{5, 111}}},
			GET, 5, 0, 111, nil),
		// get miss: empty window; get(9) -> 0.
		dictCase("dict_get_miss", p, nil, GET, 9, 0, 0, nil),
		// insert into empty: insert(7,222) -> 1, slot 7 = {7,222}.
		dictCase("dict_insert", p, nil, INS, 7, 222, 1,
			[]evolution.SeedWrite{{At: at(slot(7)), U32: []uint32{7, 222}}}),
		// overwrite: slot 3 pre-seeded {3,1}; insert(3,99) -> slot 3 = {3,99}.
		dictCase("dict_overwrite", p,
			[]evolution.SeedWrite{{At: at(slot(3)), U32: []uint32{3, 1}}},
			INS, 3, 99, 1, []evolution.SeedWrite{{At: at(slot(3)), U32: []uint32{3, 99}}}),
		// collision get: key 1 at its home slot, key 513 (same home, %512==1) probed one slot
		// on; get(513) must PROBE past key 1 to find 200.
		dictCase("dict_collision_get", p,
			[]evolution.SeedWrite{
				{At: at(slot(1)), U32: []uint32{1, 100}},
				{At: at(slot(1) + 8), U32: []uint32{513, 200}},
			},
			GET, 513, 0, 200, nil),
		// collision insert: slot 1 occupied by key 1; insert(513,200) must probe to the next
		// slot and land {513,200} there (not clobber key 1's slot).
		dictCase("dict_collision_insert", p,
			[]evolution.SeedWrite{{At: at(slot(1)), U32: []uint32{1, 100}}},
			INS, 513, 200, 1,
			[]evolution.SeedWrite{{At: at(slot(1) + 8), U32: []uint32{513, 200}}}),
	}}
}

// dictCase builds one dict scenario: seed the op buffer [op,key,val] at p plus any pre-state,
// run run-tick, and assert the result and any resulting slot.
func dictCase(name string, p uint32, pre []evolution.SeedWrite, op, key, val uint32, wantResult int32, wantReads []evolution.SeedWrite) evolution.Scenario {
	seed := append([]evolution.SeedWrite{{At: at(p), U32: []uint32{op, key, val}}}, pre...)
	return evolution.Scenario{
		Name: name, Entry: "run-tick", Steps: 1,
		Seed:   seed,
		Expect: evolution.ScenarioExpect{Result: i32p(wantResult), Reads: wantReads},
	}
}

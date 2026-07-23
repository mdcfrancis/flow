package appgen

import (
	"github.com/mdcfrancis/flow/evolution"
	"github.com/mdcfrancis/flow/execution"
)

// ListAcceptance pins sys:list behavior — single ops against a seeded window state (length at
// window[0], elements from window[4]). A mutation that drops the bounds check or mis-indexes
// fails. Op buffer [op, x] at payload offset p (op 0=LEN, 1=APPEND, 2=GET).
func ListAcceptance(p uint32) *evolution.AcceptanceSuite {
	const base = execution.DictWindowBase // shared window base
	elem := func(i uint32) uint32 { return base + 4 + i*4 }
	const LEN, APP, GET = 0, 1, 2
	return &evolution.AcceptanceSuite{Scenarios: []evolution.Scenario{
		// len of empty (zeroed window) -> 0.
		collCase("list_len_empty", p, nil, LEN, 0, 0, nil),
		// len from a seeded header.
		collCase("list_len_seeded", p, []evolution.SeedWrite{{At: at(base), U32: []uint32{3}}}, LEN, 0, 3, nil),
		// append onto [10,20]: new length 3, element[2]=30, header=3.
		collCase("list_append", p, []evolution.SeedWrite{
			{At: at(base), U32: []uint32{2}},
			{At: at(elem(0)), U32: []uint32{10, 20}},
		}, APP, 30, 3, []evolution.SeedWrite{
			{At: at(base), U32: []uint32{3}},
			{At: at(elem(2)), U32: []uint32{30}},
		}),
		// get within range.
		collCase("list_get_hit", p, []evolution.SeedWrite{
			{At: at(base), U32: []uint32{3}},
			{At: at(elem(0)), U32: []uint32{10, 20, 30}},
		}, GET, 1, 20, nil),
		// get out of range -> 0.
		collCase("list_get_oob", p, []evolution.SeedWrite{
			{At: at(base), U32: []uint32{2}},
			{At: at(elem(0)), U32: []uint32{10, 20}},
		}, GET, 5, 0, nil),
	}}
}

// SetAcceptance pins sys:set behavior — contains/add with linear-probe collisions. Slot i (a
// u32 key) at window + i*4; key 0 = empty. Op buffer [op, key] (op 0=CONTAINS, 1=ADD).
func SetAcceptance(p uint32) *evolution.AcceptanceSuite {
	slot := func(k uint32) uint32 { return execution.DictWindowBase + (k%execution.SetCap)*4 }
	const HAS, ADD = 0, 1
	return &evolution.AcceptanceSuite{Scenarios: []evolution.Scenario{
		collCase("set_contains_hit", p, []evolution.SeedWrite{{At: at(slot(5)), U32: []uint32{5}}}, HAS, 5, 1, nil),
		collCase("set_contains_miss", p, nil, HAS, 9, 0, nil),
		collCase("set_add", p, nil, ADD, 7, 1, []evolution.SeedWrite{{At: at(slot(7)), U32: []uint32{7}}}),
		collCase("set_add_idempotent", p, []evolution.SeedWrite{{At: at(slot(3)), U32: []uint32{3}}}, ADD, 3, 1,
			[]evolution.SeedWrite{{At: at(slot(3)), U32: []uint32{3}}}),
		// collision: key 1 home, key 513 (%512==1) probed one slot on.
		collCase("set_collision_contains", p, []evolution.SeedWrite{
			{At: at(slot(1)), U32: []uint32{1}},
			{At: at(slot(1) + 4), U32: []uint32{513}},
		}, HAS, 513, 1, nil),
		collCase("set_collision_add", p, []evolution.SeedWrite{{At: at(slot(1)), U32: []uint32{1}}}, ADD, 513, 1,
			[]evolution.SeedWrite{{At: at(slot(1) + 4), U32: []uint32{513}}}),
	}}
}

// collCase builds one collection scenario: seed [op, x] at p plus any pre-state, run run-tick,
// assert the result and any resulting memory.
func collCase(name string, p uint32, pre []evolution.SeedWrite, op, x uint32, wantResult int32, wantReads []evolution.SeedWrite) evolution.Scenario {
	seed := append([]evolution.SeedWrite{{At: at(p), U32: []uint32{op, x}}}, pre...)
	return evolution.Scenario{
		Name: name, Entry: "run-tick", Steps: 1,
		Seed:   seed,
		Expect: evolution.ScenarioExpect{Result: i32p(wantResult), Reads: wantReads},
	}
}

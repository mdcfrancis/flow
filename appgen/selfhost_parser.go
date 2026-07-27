package appgen

import (
	"fmt"

	"github.com/mdcfrancis/flow/evolution"
	"github.com/mdcfrancis/flow/execution"
)

// SelfHostParserAcceptance returns behavior-pinning acceptance suites for the
// self-hosted lexer and parser cells, keyed by URN. They enrol the cells in evolution
// (evolvable toward LOWER FUEL) while REJECTING any mutation that changes the parse —
// so the language's own parser can get leaner but never wrong. Offsets match the cells'
// compiled layout (the exported execution.SelfHost* region).
func SelfHostParserAcceptance() map[string]*evolution.AcceptanceSuite {
	off := func(o uint32) string { return fmt.Sprintf("0x%X", o) }
	u := func(v int32) uint32 { return uint32(v) } // encode a negative op-code token

	// Lexer: source bytes in `src` → token stream + output count. Bytes are ASCII
	// (48-57 digits, 43/45/42/47 = + - * /, 32 space skipped).
	lex := &evolution.AcceptanceSuite{Scenarios: []evolution.Scenario{
		{
			Name:  "lex 34+",
			Seed:  []evolution.SeedWrite{{At: off(execution.SelfHostSrc), U32: []uint32{51, 52, 43}}},
			Steps: 3,
			Expect: evolution.ScenarioExpect{Reads: []evolution.SeedWrite{
				{At: off(execution.SelfHostOutp), U32: []uint32{3}},
				{At: off(execution.SelfHostTok), U32: []uint32{3, 4, u(-1)}},
			}},
		},
		{
			Name:  "lex 9 3 / with spaces",
			Seed:  []evolution.SeedWrite{{At: off(execution.SelfHostSrc), U32: []uint32{57, 32, 51, 32, 47}}},
			Steps: 5,
			Expect: evolution.ScenarioExpect{Reads: []evolution.SeedWrite{
				{At: off(execution.SelfHostOutp), U32: []uint32{3}},
				{At: off(execution.SelfHostTok), U32: []uint32{9, 3, u(-4)}},
			}},
		},
	}}

	// Reducer: token stream in `tokens` → evaluated result at stack[0]. Shift numbers,
	// reduce operators (-1 + · -2 - · -3 * · -4 /).
	rpn := &evolution.AcceptanceSuite{Scenarios: []evolution.Scenario{
		{
			Name:  "reduce 3 4 +",
			Seed:  []evolution.SeedWrite{{At: off(execution.SelfHostTok), U32: []uint32{3, 4, u(-1)}}},
			Steps: 3,
			Expect: evolution.ScenarioExpect{Reads: []evolution.SeedWrite{
				{At: off(execution.SelfHostStk), U32: []uint32{7}},
			}},
		},
		{
			Name:  "reduce (3+4)*2",
			Seed:  []evolution.SeedWrite{{At: off(execution.SelfHostTok), U32: []uint32{3, 4, u(-1), 2, u(-3)}}},
			Steps: 5,
			Expect: evolution.ScenarioExpect{Reads: []evolution.SeedWrite{
				{At: off(execution.SelfHostStk), U32: []uint32{14}},
			}},
		},
		{
			Name:  "reduce 8 2 /",
			Seed:  []evolution.SeedWrite{{At: off(execution.SelfHostTok), U32: []uint32{8, 2, u(-4)}}},
			Steps: 3,
			Expect: evolution.ScenarioExpect{Reads: []evolution.SeedWrite{
				{At: off(execution.SelfHostStk), U32: []uint32{4}},
			}},
		},
	}}

	return map[string]*evolution.AcceptanceSuite{
		execution.SelfHostLexerURN:  lex,
		execution.SelfHostParserURN: rpn,
	}
}

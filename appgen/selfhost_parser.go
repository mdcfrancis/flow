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

	// AST parser: a compute-cell TOKEN stream → AST node records + write/let pairs.
	// Token encoding: field f(i)=-100-i, op (add=-1), write wr(i)=-200-i, let-bind
	// lb(i)=-400-i, local-ref lref(i)=-300-i. Node tags: 99=field 98=local 1-20=op.
	ff := func(i int32) uint32 { return u(-100 - i) }
	wrt := func(i int32) uint32 { return u(-200 - i) }
	ast := &evolution.AcceptanceSuite{Scenarios: []evolution.Scenario{
		{
			// (write (ball_x (+ ball_x vel_x)))  →  nodes: field0, field2, (+ h0 h1); write(f0,h2)
			Name:  "ast write of a binary op",
			Seed:  []evolution.SeedWrite{{At: off(execution.SelfHostASTTok), U32: []uint32{ff(0), ff(2), u(-1), wrt(0)}}},
			Steps: 4,
			Expect: evolution.ScenarioExpect{Reads: []evolution.SeedWrite{
				{At: off(execution.SelfHostASTNC), U32: []uint32{3}},
				{At: off(execution.SelfHostASTWC), U32: []uint32{1}},
				{At: off(execution.SelfHostASTNodes), U32: []uint32{99, 0, 0, 0, 99, 2, 0, 0, 1, 0, 1, 0}},
				{At: off(execution.SelfHostASTWbuf), U32: []uint32{0, 2}},
			}},
		},
		{
			// (let ([t0 (+ ball_x vel_x)]) (write (ball_x t0)))
			Name:  "ast let then write of the local",
			Seed:  []evolution.SeedWrite{{At: off(execution.SelfHostASTTok), U32: []uint32{ff(0), ff(2), u(-1), u(-400), u(-300), wrt(0)}}},
			Steps: 6,
			Expect: evolution.ScenarioExpect{Reads: []evolution.SeedWrite{
				{At: off(execution.SelfHostASTNC), U32: []uint32{4}},
				{At: off(execution.SelfHostASTLC), U32: []uint32{1}},
				{At: off(execution.SelfHostASTWC), U32: []uint32{1}},
				{At: off(execution.SelfHostASTLbuf), U32: []uint32{0, 2}},
				{At: off(execution.SelfHostASTWbuf), U32: []uint32{0, 3}},
			}},
		},
	}}

	return map[string]*evolution.AcceptanceSuite{
		execution.SelfHostLexerURN:     lex,
		execution.SelfHostParserURN:    rpn,
		execution.SelfHostASTParserURN: ast,
	}
}

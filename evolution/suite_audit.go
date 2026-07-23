package evolution

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Suite evolution rests on a single adversarial gate: any proposed acceptance
// suite (after additions, removals, or corrections by a proposer) is re-judged
// by an auditor LLM against the cell's REQUIRED BEHAVIOR. The certified suite is
// whatever the auditor deems a faithful encoding of that requirement — so tests
// can be freely added, dropped, or corrected as the objective evolves, always
// re-validated. The auditor judges fidelity ONLY, blind to whether any
// implementation passes; that is what keeps removal safe (a test is dropped only
// when it does not reflect the requirement, never merely because the cell fails
// it) and stops "delete the failing test" gaming.

// SuiteAuditPrompt steers the adversarial acceptance-test auditor.
const SuiteAuditPrompt = `You are a STRICT, ADVERSARIAL acceptance-test auditor. You are given the
REQUIRED BEHAVIOR of a component and a set of proposed acceptance checks, each
with a name. Decide, for EACH check, whether it is a faithful and correct
encoding of the required behavior.

Judge ONLY fidelity to the required behavior:
- APPROVE a check only if its expectation is correct per the requirement AND the
  requirement actually implies it.
- REJECT a check that is wrong, self-contradictory, unrelated to the requirement,
  or that asserts something the requirement does not justify.
- Do NOT consider whether any implementation passes or fails a check. A check the
  current code fails may still be correct (the code is wrong, not the test); a
  check the code passes may still be wrong. Judge the check against the
  REQUIREMENT alone.

Output ONLY JSON, no prose or fences:
{"verdicts":[{"name":"<check name>","faithful":true,"reason":"<short>"}]}
Exactly one verdict per input check, keyed by its name.`

// AuditVerdict is the auditor's ruling on one acceptance check.
type AuditVerdict struct {
	Name     string `json:"name"`
	Faithful bool   `json:"faithful"`
	Reason   string `json:"reason"`
}

type auditResponse struct {
	Verdicts []AuditVerdict `json:"verdicts"`
}

// describeChecks renders the suite as a compact, name-keyed listing the auditor
// judges. Tests are int-in/int-out; scenarios are behavioral (input seed + a
// draw-stream assertion), summarized in prose.
func describeChecks(suite *AcceptanceSuite) []map[string]string {
	var out []map[string]string
	for _, tc := range suite.Tests {
		out = append(out, map[string]string{
			"name": tc.Name,
			"kind": "test",
			"spec": fmt.Sprintf("input %d -> expected %d", tc.Input, tc.Expected),
		})
	}
	for _, sc := range suite.Scenarios {
		out = append(out, map[string]string{
			"name": sc.Name,
			"kind": "scenario",
			"spec": describeScenario(sc),
		})
	}
	return out
}

func describeScenario(sc Scenario) string {
	var b strings.Builder
	// Show the EXACT seed writes (offset=value[s]) so the builder knows the input
	// state — e.g. that the HMI keydown carries key code 68, or player_x starts at
	// 100. "seed input register" alone told the model nothing and left coordination
	// cells unbuildable.
	if len(sc.Seed) > 0 {
		b.WriteString("seed ")
		for i, w := range sc.Seed {
			if i > 0 {
				b.WriteString(", ")
			}
			fmt.Fprintf(&b, "mem[%s]=%s", w.At, joinU32s(w.U32))
		}
		b.WriteString("; ")
	}
	if sc.Steps > 1 {
		fmt.Fprintf(&b, "over %d frames, ", sc.Steps)
	}
	if sc.Expect.Result != nil {
		fmt.Fprintf(&b, "expect result %d; ", *sc.Expect.Result)
	}
	// The `reads` postcondition IS the coordination spec: after running, these
	// shared-memory fields must hold these exact values. Rendering it is what lets
	// the builder implement the coordination (e.g. "on key 68, set player_x=105").
	if len(sc.Expect.Reads) > 0 {
		b.WriteString("then expect ")
		for i, r := range sc.Expect.Reads {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(describeRead(r))
		}
		b.WriteString("; ")
	}
	if d := sc.Expect.Draw; d != nil {
		if d.MinRecords > 0 {
			fmt.Fprintf(&b, "expect >=%d %s primitives; ", d.MinRecords, orAny(d.Op))
		}
		if d.MinColors > 0 {
			fmt.Fprintf(&b, "expect >=%d DISTINCT colors (map each value to its own color, not a flat fill); ", d.MinColors)
		}
		if d.MinBrightSpread > 0 {
			fmt.Fprintf(&b, "expect the colors to span dark→bright (brightness range >=%d of 255 — a high-contrast gradient, e.g. extreme/sentinel values dark, others bright); ", d.MinBrightSpread)
		}
		if d.NearX != nil {
			fmt.Fprintf(&b, "expect a %s drawn near x=%d; ", orAny(d.Op), *d.NearX)
		}
		if d.NearY != nil {
			fmt.Fprintf(&b, "expect a %s drawn near y=%d; ", orAny(d.Op), *d.NearY)
		}
		if d.Moved != "" {
			fmt.Fprintf(&b, "expect the drawn sprite to move %s; ", d.Moved)
		}
	}
	if b.Len() == 0 {
		return "renders a frame"
	}
	return strings.TrimSuffix(b.String(), "; ")
}

// describeRead renders one reads postcondition per its comparison operator, so
// the builder sees the INTENT (e.g. "mem[0xB0000] increased") rather than an
// arbitrary exact value.
func describeRead(r SeedWrite) string {
	switch r.Cmp {
	case "increased", "decreased", "changed", "unchanged":
		return fmt.Sprintf("mem[%s] %s", r.At, r.Cmp)
	case "gt":
		return fmt.Sprintf("mem[%s]>%s", r.At, joinU32s(r.U32))
	case "lt":
		return fmt.Sprintf("mem[%s]<%s", r.At, joinU32s(r.U32))
	case "ge":
		return fmt.Sprintf("mem[%s]>=%s", r.At, joinU32s(r.U32))
	case "le":
		return fmt.Sprintf("mem[%s]<=%s", r.At, joinU32s(r.U32))
	case "ne":
		return fmt.Sprintf("mem[%s]!=%s", r.At, joinU32s(r.U32))
	default:
		return fmt.Sprintf("mem[%s]==%s", r.At, joinU32s(r.U32))
	}
}

// joinU32s renders a seed/reads value list as a compact decimal (with hex for the
// first when it looks like a memory offset would be clearer). Consecutive values
// mean consecutive i32 slots from the base offset.
func joinU32s(vs []uint32) string {
	if len(vs) == 1 {
		return fmt.Sprintf("%d", vs[0])
	}
	parts := make([]string, len(vs))
	for i, v := range vs {
		parts[i] = fmt.Sprintf("%d", v)
	}
	return "[" + strings.Join(parts, ",") + "]"
}

func orAny(op string) string {
	if op == "" {
		return "drawn"
	}
	return op
}

// ValidateSuite runs the adversarial auditor over a proposed suite and returns
// the certified subset (the faithful checks) plus the full verdict list. A check
// is dropped ONLY on an explicit faithful:false verdict; a check the auditor
// omits or leaves ambiguous is RETAINED, so removal always requires a grounded
// adversarial rejection (never silent loss). On a reasoning error the input
// suite is returned unchanged (fail-open: never lose tests to transport faults).
func ValidateSuite(ctx context.Context, model Reasoner, requirement string, suite *AcceptanceSuite) (*AcceptanceSuite, []AuditVerdict, error) {
	if suite == nil || (len(suite.Tests) == 0 && len(suite.Scenarios) == 0) {
		return suite, nil, nil
	}
	checks := describeChecks(suite)
	payload, _ := json.Marshal(map[string]any{"required_behavior": requirement, "checks": checks})

	resp, err := model.InvokeReasoning(ctx, SuiteAuditPrompt, string(payload))
	if err != nil {
		return suite, nil, fmt.Errorf("suite audit reasoning failed: %w", err)
	}
	js := extractJSONObject(resp)
	if js == "" {
		return suite, nil, fmt.Errorf("no JSON in audit response")
	}
	var ar auditResponse
	if err := json.Unmarshal([]byte(js), &ar); err != nil {
		return suite, nil, fmt.Errorf("decode audit verdicts: %w", err)
	}

	// A check is rejected only on an explicit faithful:false verdict.
	rejected := make(map[string]bool)
	for _, v := range ar.Verdicts {
		if !v.Faithful {
			rejected[v.Name] = true
		}
	}
	kept := &AcceptanceSuite{}
	for _, tc := range suite.Tests {
		if !rejected[tc.Name] {
			kept.Tests = append(kept.Tests, tc)
		}
	}
	for _, sc := range suite.Scenarios {
		// A COORDINATION scenario is faithful by construction — its expectation IS
		// the coordination behavior the builder must implement, not something
		// derived from a looser requirement. The requirement-fidelity audit flags
		// such expectations as "unjustified" and would nuke the whole suite (seen
		// eroding cells to a vacuous 0/0), so never drop one. Two forms qualify: a
		// `reads` postcondition on a shared field, and a draw POSITION assertion
		// (nearX/nearY) that ties a rendered sprite to a seeded shared field.
		coord := len(sc.Expect.Reads) > 0 ||
			(sc.Expect.Draw != nil && (sc.Expect.Draw.NearX != nil || sc.Expect.Draw.NearY != nil))
		if coord || !rejected[sc.Name] {
			kept.Scenarios = append(kept.Scenarios, sc)
		}
	}
	return kept, ar.Verdicts, nil
}

// extractJSONObject isolates the outermost balanced {...} object in a model
// completion, tolerating prose and code fences.
func extractJSONObject(s string) string {
	start := strings.Index(s, "{")
	if start < 0 {
		return ""
	}
	depth, inStr, esc := 0, false, false
	for i := start; i < len(s); i++ {
		c := s[i]
		switch {
		case esc:
			esc = false
		case c == '\\' && inStr:
			esc = true
		case c == '"':
			inStr = !inStr
		case inStr:
		case c == '{':
			depth++
		case c == '}':
			depth--
			if depth == 0 {
				return s[start : i+1]
			}
		}
	}
	return ""
}

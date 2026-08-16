package evolution

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"strings"
)

// acceptDebug turns on per-candidate acceptance diagnostics (HDM_ACCEPT_DEBUG):
// when a candidate fails to improve, the orchestrator logs WHY each scenario
// failed — the specific check, expected vs actual — so a stall can be traced to a
// concrete cause (wrong logic, a field never changing, a trap) instead of a bare
// score.
var acceptDebug = os.Getenv("HDM_ACCEPT_DEBUG") != ""

// SuiteFailureReasons runs a suite's scenarios against a phenotype and returns a
// human-readable reason for each FAILING scenario (an exec trap, or the first
// unmet expectation with expected-vs-actual values). Used only for diagnostics.
func SuiteFailureReasons(ctx context.Context, phenotype []byte, entry string, suite *AcceptanceSuite, payloadOffset, stateWindow uint32, resolver CellResolver, mask ...*FieldMask) []string {
	if suite == nil {
		return nil
	}
	var out []string
	// Scalar tests: int-in / int-out on the entry's RETURN value. For a
	// state-transforming run-tick cell this grades the wrong thing (it returns a
	// status and mutates shared state), so a failing scalar test on such a cell is
	// itself a spec smell — surface it explicitly.
	for _, tc := range suite.Tests {
		payload := make([]byte, 4)
		binary.LittleEndian.PutUint32(payload, uint32(tc.Input))
		env := replayEnv{payloadOffset: payloadOffset, payload: payload, stateWindow: stateWindow, resolver: resolver}
		res, err := execReplay(ctx, phenotype, entry, env, uint64(payloadOffset), uint64(len(payload)))
		if err != nil || len(res.Results) == 0 {
			out = append(out, fmt.Sprintf("test %q: input=%d no result (%v)", tc.Name, tc.Input, err))
			continue
		}
		if uint32(res.Results[0]) != uint32(tc.Expected) {
			out = append(out, fmt.Sprintf("test %q: run-tick(%d) returned %d, expected %d", tc.Name, tc.Input, int32(uint32(res.Results[0])), tc.Expected))
		}
	}
	for _, sc := range suite.Scenarios {
		results, frames, reads, pre, traj, err := execScenario(ctx, phenotype, sc, payloadOffset, stateWindow, resolver, mask...)
		if err != nil {
			out = append(out, fmt.Sprintf("scenario %q: EXEC ERROR: %v", sc.Name, err))
			continue
		}
		if ok, reason := matchScenarioReason(sc, results, frames, reads, pre, traj); !ok {
			out = append(out, fmt.Sprintf("scenario %q [%s]: %s", sc.Name, entryOrDefault(sc.Entry), reason))
		}
	}
	return out
}

func entryOrDefault(e string) string {
	if e == "" {
		return "run-tick"
	}
	return e
}

// matchScenarioReason mirrors matchScenario exactly but returns, on failure, the
// first unmet expectation with expected-vs-actual detail. matchScenario delegates
// to it so the two can never diverge.
func matchScenarioReason(sc Scenario, results []uint64, frames [][]DrawRecord, reads, pre, traj [][]uint32) (bool, string) {
	e := sc.Expect
	for i, rd := range e.Reads {
		if i >= len(reads) {
			return false, fmt.Sprintf("%s not captured", readTarget(rd))
		}
		var before []uint32
		if i < len(pre) {
			before = pre[i]
		}
		if !matchRead(rd, before, reads[i]) {
			return false, fmt.Sprintf("%s: expected %s, before=%s after=%s",
				readTarget(rd), expectDesc(rd), words(before), words(reads[i]))
		}
	}
	if e.Result != nil {
		got := int32(0)
		if len(results) > 0 {
			got = int32(uint32(results[0]))
		}
		if len(results) == 0 || got != *e.Result {
			return false, fmt.Sprintf("result: expected %d, got %d", *e.Result, got)
		}
	}
	if e.Draw != nil && !matchDraw(*e.Draw, frames) {
		return false, fmt.Sprintf("draw: %s not satisfied (%d record(s) drawn)", drawDesc(*e.Draw), lastFrameLen(frames))
	}
	for i, t := range e.Trajectory {
		var seq []uint32
		if i < len(traj) {
			seq = traj[i]
		}
		if i >= len(traj) || !matchTrajectory(t, seq) {
			return false, fmt.Sprintf("trajectory %s: %s, got seq=%s", t.At, trajDesc(t), words(seq))
		}
	}
	return true, ""
}

func readTarget(rd SeedWrite) string {
	if rd.Field != "" {
		return rd.Field
	}
	return rd.At
}

func expectDesc(rd SeedWrite) string {
	switch rd.Cmp {
	case "", "eq":
		return "== " + words(rd.U32)
	case "increased", "decreased", "changed", "unchanged":
		return rd.Cmp
	case "gt", "lt", "ge", "le", "ne":
		v := ""
		if len(rd.U32) > 0 {
			v = fmt.Sprintf("%d", int32(rd.U32[0]))
		}
		return rd.Cmp + " " + v
	}
	return rd.Cmp
}

func drawDesc(d DrawExpect) string {
	var p []string
	if d.Op != "" {
		p = append(p, "op="+d.Op)
	}
	if d.MinRecords > 0 {
		p = append(p, fmt.Sprintf("minRecords=%d", d.MinRecords))
	}
	if d.NearX != nil {
		p = append(p, fmt.Sprintf("nearX=%d", *d.NearX))
	}
	if d.NearY != nil {
		p = append(p, fmt.Sprintf("nearY=%d", *d.NearY))
	}
	if d.MinColors > 0 {
		p = append(p, fmt.Sprintf("minColors=%d", d.MinColors))
	}
	return strings.Join(p, " ")
}

func trajDesc(t TrajectoryExpect) string {
	var p []string
	if t.MinDistinct > 0 {
		p = append(p, fmt.Sprintf("minDistinct=%d", t.MinDistinct))
	}
	if t.InBoundsMin != nil {
		p = append(p, fmt.Sprintf(">=%d", *t.InBoundsMin))
	}
	if t.InBoundsMax != nil {
		p = append(p, fmt.Sprintf("<=%d", *t.InBoundsMax))
	}
	return strings.Join(p, " ")
}

func words(u []uint32) string {
	if len(u) == 0 {
		return "[]"
	}
	s := make([]string, len(u))
	for i, w := range u {
		s[i] = fmt.Sprintf("%d", int32(w))
	}
	return "[" + strings.Join(s, ",") + "]"
}

func lastFrameLen(frames [][]DrawRecord) int {
	if len(frames) == 0 {
		return 0
	}
	return len(frames[len(frames)-1])
}

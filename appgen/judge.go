package appgen

// The JUDGMENT tier is HDM's scarce, sparing use of the model to settle what free
// signals can't. Free signals (gauntlet score, distinct-value counts) are nearly
// free and drive almost everything; an LLM judgment is a costly call, so it is
// spent only where a cheap signal is genuinely ambiguous. Its ONLY power is to
// author a STRONGER acceptance check — it never overrides the gauntlet, which stays
// the authority. Here it judges emergent MOTION: is a moving element's per-step
// path a smooth in-window traversal, or a degenerate artifact (teleporting between
// extremes, frozen, or oscillating between two values) that games the coarser
// trajectory checks? A "fast-but-valid" dot and a "teleporting" dot can have
// similar distinct-value counts; the qualitative call is what the model is for.

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/mdcfrancis/flow/evolution"
)

const motionJudgePrompt = `You judge whether a moving element's per-step POSITION PATH is a smooth traversal
of its window, or a DEGENERATE artifact that only appears to move.

Degenerate paths (the element does not really glide):
- frozen: the value never changes;
- two-value oscillation: it flips between two values (e.g. 0, 600, 0, 600 …) —
  teleporting corner to corner, not traversing;
- teleporting: huge per-step jumps that cross most of the window in one step.

A GOOD path advances in small steps and, on hitting a boundary, reverses and keeps
advancing — visiting many intermediate positions.

Given the objective and each field's path (per-step values), decide. Output ONLY
JSON, no prose: {"degenerate": <bool>, "reason": "<short>"}`

// probeSteps is how many steps the motion probe runs — long enough that a
// two-value oscillation is unmistakable versus a genuine traversal.
const probeSteps = 16

// glideDistinct is the free-signal threshold: a path with at least this many
// distinct values across probeSteps is clearly traversing, so no model call is
// spent. Below it, the path is ambiguous (could be slow-but-valid or degenerate)
// and worth a judgment.
const glideDistinct = 12

// strongerDistinct is the MinDistinct a judged-degenerate field's stronger check
// demands — well above a teleporter's 2–3, safely below a genuine glider's ~probeSteps
// (a bouncer that turns around still clears it), so it re-opens the cell toward a
// real integrator without rejecting correct motion.
const strongerDistinct = probeSteps / 2

// JudgeMotion probes a run-tick cell's autonomous motion, and if a position field's
// path is degenerate (confirmed by a sparing model judgment), authors a STRONGER
// trajectory check on it and returns the number of checks added (so the caller can
// re-open the cell). Free-first: it spends a model call only when the cheap
// distinct-value signal is ambiguous. Returns 0 (no change) when the motion already
// glides, the cell isn't an autonomous mover, or the model faults.
func (g *Grower) JudgeMotion(ctx context.Context, cellURN string) (int, error) {
	suite, _ := evolution.LoadAcceptance(g.ledger, cellURN)
	if suite == nil {
		return 0, nil
	}
	seed, movedOffsets := autonomousMovement(suite)
	if len(movedOffsets) == 0 {
		return 0, nil // not an autonomous mover (e.g. input/event or static cell)
	}
	desc, err := g.repo.Load(cellURN)
	if err != nil {
		return 0, err
	}
	phenotype, err := g.repo.Phenotype(desc)
	if err != nil {
		return 0, err
	}
	// Probe: run the accepted (no-collision, no-input) seed for many steps, capturing
	// each moved field's path.
	traj := make([]evolution.TrajectoryExpect, 0, len(movedOffsets))
	for _, off := range movedOffsets {
		traj = append(traj, evolution.TrajectoryExpect{At: off})
	}
	probe := evolution.Scenario{
		Name: "motion_probe", Seed: seed, Steps: probeSteps, Entry: "run-tick",
		Expect: evolution.ScenarioExpect{Trajectory: traj},
	}
	resolver := func(urn string) ([]byte, bool) {
		d, e := g.repo.Load(urn)
		if e != nil {
			return nil, false
		}
		bc, e := g.repo.Phenotype(d)
		return bc, e == nil
	}
	paths, err := evolution.RunTrajectory(ctx, phenotype, probe, evolution.DefaultPayloadOffset, evolution.DefaultStateWindow, resolver)
	if err != nil || len(paths) == 0 {
		return 0, err
	}

	// Free-first: which fields look suspicious (few distinct values)?
	var suspicious []int
	for i, p := range paths {
		if distinctU32(p) < glideDistinct {
			suspicious = append(suspicious, i)
		}
	}
	if len(suspicious) == 0 {
		return 0, nil // clearly gliding — no judgment needed
	}

	// Spend one judgment to confirm degeneracy before strengthening — so a
	// slow-but-valid path isn't wrongly re-opened.
	objective := ""
	if env := LoadEnvelope(g.ledger, evolution.AppNamespaceOf(cellURN)); env != nil {
		objective = env.Objective
	}
	if !g.judgeDegenerate(ctx, objective, movedOffsets, paths, suspicious) {
		return 0, nil
	}

	// Author the stronger check(s): require the suspicious field(s) to visit many
	// distinct positions — un-satisfiable by a teleport/oscillation, satisfiable by
	// a real glide. Skip a field that already carries such a check.
	added := 0
	for _, i := range suspicious {
		off := movedOffsets[i]
		name := "glides_smoothly_" + strings.TrimPrefix(off, "0x")
		if hasScenarioNamed(suite, name) {
			continue
		}
		suite.Scenarios = append(suite.Scenarios, evolution.Scenario{
			Name: name, Seed: seed, Steps: probeSteps, Entry: "run-tick",
			Expect: evolution.ScenarioExpect{Trajectory: []evolution.TrajectoryExpect{
				{At: off, MinDistinct: strongerDistinct},
			}},
		})
		added++
	}
	if added == 0 {
		return 0, nil
	}
	if err := evolution.SaveAcceptance(g.ledger, cellURN, suite); err != nil {
		return 0, err
	}
	g.event("create", cellURN, fmt.Sprintf("motion judged degenerate — authored %d stronger trajectory check(s)", added))
	return added, nil
}

// autonomousMovement returns the seed and the resolved offsets of the position
// fields of an accepted, non-input, single-tick movement scenario — the basis for a
// motion probe. Empty when the cell shows no autonomous per-tick movement.
func autonomousMovement(suite *evolution.AcceptanceSuite) (seed []evolution.SeedWrite, offsets []string) {
	for _, sc := range suite.Scenarios {
		if sc.Entry == "render-frame" || len(sc.Seed) == 0 {
			continue
		}
		inputDriven := false
		for _, sd := range sc.Seed {
			if off, err := parseOffset(sd.At); err == nil && off >= hmiInputLo && off < hmiInputHi {
				inputDriven = true
				break
			}
		}
		if inputDriven {
			continue
		}
		var offs []string
		for _, r := range sc.Expect.Reads {
			switch r.Cmp {
			case "increased", "decreased", "changed":
				offs = append(offs, r.At)
			}
		}
		if len(offs) > 0 {
			return sc.Seed, offs
		}
	}
	return nil, nil
}

// judgeDegenerate asks the model whether any suspicious field's path is degenerate.
func (g *Grower) judgeDegenerate(ctx context.Context, objective string, offsets []string, paths [][]uint32, suspicious []int) bool {
	var b strings.Builder
	fmt.Fprintf(&b, "objective: %s\n", objective)
	for _, i := range suspicious {
		fmt.Fprintf(&b, "field %s path: %s\n", offsets[i], formatPath(paths[i]))
	}
	resp, err := g.model.InvokeReasoning(ctx, g.prompt("motion-judge", motionJudgePrompt), b.String())
	if err != nil {
		return false // model unavailable — do not strengthen on a guess
	}
	js := extractJSON(resp)
	if js == "" {
		return false
	}
	var v struct {
		Degenerate bool `json:"degenerate"`
	}
	if json.Unmarshal([]byte(js), &v) != nil {
		return false
	}
	return v.Degenerate
}

// formatPath renders a value sequence for the judge, showing each value as both a
// signed integer and (in case the cell stores floats) its float32 interpretation.
func formatPath(p []uint32) string {
	parts := make([]string, 0, len(p))
	for _, v := range p {
		f := math.Float32frombits(v)
		if !math.IsNaN(float64(f)) && !math.IsInf(float64(f), 0) && math.Abs(float64(f)) < 1e7 && f != float32(int32(v)) {
			parts = append(parts, fmt.Sprintf("%d(=%.1ff)", int32(v), f))
		} else {
			parts = append(parts, strconv.Itoa(int(int32(v))))
		}
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func distinctU32(p []uint32) int {
	seen := map[uint32]struct{}{}
	for _, v := range p {
		seen[v] = struct{}{}
	}
	return len(seen)
}

func hasScenarioNamed(suite *evolution.AcceptanceSuite, name string) bool {
	for _, sc := range suite.Scenarios {
		if sc.Name == name {
			return true
		}
	}
	return false
}

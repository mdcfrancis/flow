package appgen

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"

	"github.com/mdcfrancis/flow/evolution"
	"github.com/mdcfrancis/flow/storage"
)

// maxSuiteChecks bounds a cell's acceptance suite so expansion cannot grow it
// without limit; maxSubsystems bounds an application's architecture likewise.
const (
	maxSuiteChecks = 16
	maxSubsystems  = 8
)

// architectureCriticPrompt steers the adversarial architecture-completeness
// critic: given the objective and the current subsystems, is anything missing?
const architectureCriticPrompt = `You are a STRICT, ADVERSARIAL software architect judging whether an
application's architecture is COMPLETE for its stated objective. You are given
the OBJECTIVE, the current SUBSYSTEMS (each a single-responsibility cell), and the
APPLICATION MAP — what each component VERIFIABLY does and how shared state flows
between them.

Decide honestly against the OBJECTIVE ALONE:
- If the current subsystems fully deliver the objective, say it is complete.
- If a subsystem is genuinely MISSING, propose the SINGLE most important one — the
  smallest useful addition — as a new single-responsibility cell.
- Use the map's DATA FLOW as evidence: a shared field written by a component and
  read by NOBODY usually means a consumer is missing, and a component that reads
  nothing may be disconnected from the system.
- Do NOT invent scope the objective does not ask for (no speculative features).
  When in doubt, prefer "complete".

Output ONLY JSON, no prose or fences:
{"complete": true}
or
{"complete": false, "subsystem": {"identity":"<namespace>:<short-part>", "semantics":"<one thing it does>"}}`

type archVerdict struct {
	Complete  bool       `json:"complete"`
	Subsystem *Subsystem `json:"subsystem"`
}

// ChallengeArchitecture asks the adversarial critic whether an application's
// architecture is complete for its objective. If a requirement-faithful missing
// subsystem is proposed, it is scaffolded and enrolled (so the parallel loop
// builds it), the envelope is extended, and 1 is returned. A no-op (0) when the
// app is judged complete, is at the subsystem cap, or has no stored envelope.
// On a reasoning fault nothing is changed.
func (g *Grower) ChallengeArchitecture(ctx context.Context, namespace string, enroll func(urn string)) (int, error) {
	env := LoadEnvelope(g.ledger, namespace)
	if env == nil || strings.TrimSpace(env.Objective) == "" {
		return 0, nil
	}
	if len(env.SubsystemRequirements) >= maxSubsystems {
		return 0, nil
	}

	g.phase("architecting", "challenging whether "+namespace+" is complete", namespace)
	subs := make([]map[string]string, 0, len(env.SubsystemRequirements))
	for _, s := range env.SubsystemRequirements {
		subs = append(subs, map[string]string{"identity": s.Identity, "semantics": s.Semantics})
	}
	payload, _ := json.Marshal(map[string]any{
		"objective": env.Objective, "namespace": namespace, "subsystems": subs,
		// The live map: what each component VERIFIABLY does and which shared fields
		// are produced but consumed by nobody — the real evidence of what is missing.
		"application_map": mapContext(g.ledger, namespace),
	})
	resp, err := g.model.InvokeReasoning(ctx, g.prompt("architecture-critic", architectureCriticPrompt), string(payload))
	if err != nil {
		return 0, err
	}
	js := extractJSON(resp)
	if js == "" {
		return 0, nil
	}
	var v archVerdict
	if json.Unmarshal([]byte(js), &v) != nil {
		return 0, nil
	}
	if v.Complete || v.Subsystem == nil || strings.TrimSpace(v.Subsystem.Semantics) == "" {
		return 0, nil
	}

	sub := *v.Subsystem
	sub.Identity = coerceIdentity(sub.Identity, namespace, env.SubsystemRequirements)
	env.SubsystemRequirements = append(env.SubsystemRequirements, sub)
	saveEnvelope(g.ledger, env)
	if err := g.scaffold(ctx, env, sub); err != nil {
		return 0, fmt.Errorf("scaffold proposed subsystem %s: %w", sub.Identity, err)
	}
	if enroll != nil {
		enroll(sub.Identity)
	}
	g.event("create", sub.Identity, "architecture extended: "+sub.Semantics)
	return 1, nil
}

// fracturePrompt steers the decomposition of a single STUCK subsystem into
// smaller single-responsibility sub-cells. This is the "break the problem into
// slices an engineer can build" move: when a cell cannot be synthesized whole,
// split its responsibility, not the app.
const fracturePrompt = `You are a senior engineer breaking a task that is TOO BIG TO BUILD IN ONE PIECE
into smaller sub-tasks. You are given the application OBJECTIVE, ONE STUCK
subsystem (its semantics and the acceptance checks it has failed to satisfy), and
the SHARED-STATE CONTRACT the subsystems coordinate through.

Decompose the STUCK subsystem into 2-3 SMALLER subsystems that TOGETHER deliver
its responsibility. Each sub-cell must:
- do exactly ONE narrow thing that is clearly easier to implement than the whole;
- coordinate ONLY through the shared contract fields (one sub-cell writes a field,
  another reads it) — never through private state;
- be a fresh urn identity under the application namespace;
- declare its BOUNDARY: the exact contract fields it reads and writes. This boundary
  is ENFORCED — a sub-cell can read only its "reads" and write only its "writes"
  (any other access is hidden/discarded), so declare every field it needs and nothing
  more. Use the literal "HMI input" for operator input.

Do NOT restate the whole subsystem as one child. Do NOT invent new scope. If the
subsystem genuinely cannot be usefully split (it already does one atomic thing),
say so.

Declare each child's KIND and keep its reads/writes consistent: "compute" WRITES
state and draws nothing; "render" READS state and draws it, writing NO state;
"input" reads "HMI input" and WRITES state; "leaf" is a pure arg-pointer function.
A child that WRITES state is never "render" — if the parent both updated and drew
state, split it into a compute writer + a render view. (Ports are validated; ports win.)

Output ONLY JSON, no prose or fences:
{"splittable": false}
or
{"splittable": true, "subsystems": [
  {"identity":"<namespace>:<short-part>", "kind":"compute|render|input|leaf", "semantics":"<one narrow thing>", "reads":["field",...], "writes":["field",...]},
  {"identity":"<namespace>:<short-part>", "kind":"compute|render|input|leaf", "semantics":"<one narrow thing>", "reads":["field",...], "writes":["field",...]}
]}`

type fractureVerdict struct {
	Splittable bool        `json:"splittable"`
	Subsystems []Subsystem `json:"subsystems"`
}

// FractureCell decomposes a stuck cell into 2-3 smaller sub-cells, each owning a
// slice of the original responsibility and coordinating through the app's
// shared-state contract. It is the stall-recovery move: an engineer breaks a task
// they cannot do in one pass into manageable pieces. The parent subsystem is
// REPLACED in the envelope by its children; the children are scaffolded (with
// their own, simpler acceptance suites) and enrolled, and the parent cell is
// retired from the annealing set via the retire callback.
//
// Returns the number of sub-cells created (0 = a no-op: no envelope, the cell is
// not a known subsystem, it is judged atomic, the app is at the subsystem cap, or
// a reasoning fault). Bounded by maxSubsystems.
func (g *Grower) FractureCell(ctx context.Context, cellURN string, enroll, retire func(urn string)) (int, error) {
	namespace := evolution.AppNamespaceOf(cellURN)
	if namespace == "" {
		return 0, nil
	}
	env := LoadEnvelope(g.ledger, namespace)
	if env == nil || strings.TrimSpace(env.Objective) == "" {
		return 0, nil
	}
	// Locate the parent subsystem in the envelope.
	parentIdx := -1
	for i, s := range env.SubsystemRequirements {
		if s.Identity == cellURN {
			parentIdx = i
			break
		}
	}
	if parentIdx < 0 {
		return 0, nil
	}
	parent := env.SubsystemRequirements[parentIdx]
	// Leave at least one slot; each child must fit under the cap.
	if len(env.SubsystemRequirements) >= maxSubsystems {
		return 0, nil
	}

	g.phase("fracturing", "decomposing stuck cell "+cellURN+" into sub-tasks", cellURN)

	// Show the model the checks the cell has failed to satisfy — the concrete
	// evidence of what it could not build whole.
	suite, _ := evolution.LoadAcceptance(g.ledger, cellURN)
	var checks []string
	if suite != nil {
		for _, t := range suite.Tests {
			checks = append(checks, fmt.Sprintf("%s: in=%d expect=%d", t.Name, t.Input, t.Expected))
		}
		for _, s := range suite.Scenarios {
			checks = append(checks, s.Name)
		}
	}
	contract := evolution.LoadContract(g.ledger, namespace)
	contractText := ""
	if contract != nil {
		contractText = contract.Render()
	}
	payload, _ := json.Marshal(map[string]any{
		"objective":       env.Objective,
		"namespace":       namespace,
		"stuck_subsystem": map[string]string{"identity": parent.Identity, "semantics": parent.Semantics},
		"failed_checks":   checks,
		"shared_contract": contractText,
		// The live map + design plan, so the split is made to fit the system that
		// already exists and its intended choreography.
		"application_map": mapContext(g.ledger, namespace),
		"system_plan":     planContext(g.ledger, namespace),
	})
	resp, err := g.model.InvokeReasoning(ctx, g.prompt("fracture", fracturePrompt), string(payload))
	if err != nil {
		return 0, err
	}
	js := extractJSON(resp)
	if js == "" {
		return 0, nil
	}
	var v fractureVerdict
	if json.Unmarshal([]byte(js), &v) != nil {
		return 0, nil
	}
	if !v.Splittable || len(v.Subsystems) < 2 {
		return 0, nil // judged atomic — nothing to fracture
	}

	// Coerce child identities to fresh, in-namespace URNs (excluding the parent,
	// which is being retired), and cap the child count to the remaining slots.
	remaining := env.SubsystemRequirements[:parentIdx:parentIdx]
	remaining = append(remaining, env.SubsystemRequirements[parentIdx+1:]...)
	room := maxSubsystems - len(remaining)
	if room < 2 {
		return 0, nil
	}
	children := v.Subsystems
	if len(children) > room {
		children = children[:room]
	}
	taken := append([]Subsystem(nil), remaining...)
	for i := range children {
		children[i].Identity = coerceIdentity(children[i].Identity, namespace, taken)
		taken = append(taken, children[i])
		if strings.TrimSpace(children[i].Semantics) == "" {
			return 0, nil
		}
	}

	// Replace the parent with its children in the envelope and persist BEFORE
	// scaffolding, so a crash mid-scaffold leaves a consistent (child-bearing)
	// architecture that the parallel loop can still build.
	env.SubsystemRequirements = append(remaining, children...)
	normalizeKinds(env) // validate each child's declared kind against its ports (ports win)
	saveEnvelope(g.ledger, env)
	// Re-read the children WITH their normalized kinds for the scaffold loop below.
	children = env.SubsystemRequirements[len(remaining):]

	// Retire the parent from the annealing set first: the children now own its
	// responsibility, and peers read the same contract fields regardless of writer.
	if retire != nil {
		retire(parent.Identity)
	}
	for _, child := range children {
		if err := g.scaffold(ctx, env, child); err != nil {
			return 0, fmt.Errorf("scaffold fractured sub-cell %s: %w", child.Identity, err)
		}
		// Author COORDINATION scenarios for the child NOW, at fracture time — not
		// at a distant fixpoint. A fractured child is a coordination cell by
		// construction: its semantics name the contract fields it reads/writes, so
		// scalar int-in/int-out tests (what genesis authors for a compute cell)
		// don't fit and leave it a purposeless 0/0. Contract-aware scenarios (with
		// `reads` postconditions) are the only checks that test coordination and
		// give the child something concrete to be built toward immediately. This
		// OVERWRITES the genesis suite so no ill-fitting scalar tests linger; a UI
		// child keeps its draw-stream scenarios. Best-effort — the fixpoint
		// curriculum remains a backstop if authoring faults.
		if contract != nil && kindOf(child) != KindRender {
			if suite := g.authorCoordination(ctx, child.Semantics, contract, namespace, kindOf(child) == KindRender, child.Writes); suiteCount(suite) > 0 {
				_ = evolution.SaveAcceptance(g.ledger, child.Identity, suite)
				g.event("create", child.Identity, fmt.Sprintf("authored %d coordination check(s)", suiteCount(suite)))
			}
		}
		if enroll != nil {
			enroll(child.Identity)
		}
	}
	g.event("split", parent.Identity, fmt.Sprintf("fractured into %d sub-cells", len(children)))
	return len(children), nil
}

// authorCoordination proposes contract-aware coordination scenarios for a cell
// from its semantics and the app's shared-state contract, then adversarially
// certifies them so only requirement-faithful checks survive. Returns nil if
// nothing is proposed, nothing survives certification, or the model faults.
func (g *Grower) authorCoordination(ctx context.Context, semantics string, contract *evolution.AppContract, namespace string, isUI bool, writes []string) *evolution.AcceptanceSuite {
	// proposeAdditional grounds coordination scenarios in the contract (resolves
	// `field` names to offsets, drops invented-offset reads) and forces each
	// scenario's entry to match the cell kind via isUI (render-frame vs run-tick).
	proposed := g.proposeAdditional(ctx, semantics, isUI, &evolution.AcceptanceSuite{}, contract, namespace)
	certified := proposed
	if suiteCount(proposed) > 0 {
		if cert, _, err := evolution.ValidateSuite(ctx, g.model, semantics, proposed); err == nil && suiteCount(cert) > 0 {
			certified = cert
		}
	}
	if certified == nil {
		certified = &evolution.AcceptanceSuite{}
	}
	// A state-writing run-tick cell is graded on the STATE IT PRODUCES, never its
	// return value: a scalar int-in/int-out test on a physics cell (which returns a
	// status and mutates shared state) can never pass, no matter how correct the
	// logic — the wall physics hit. So for such a cell, generate DIRECTIONAL motion
	// scenarios from the mock world + its declared writes, and drop the scalar tests.
	if !isUI {
		if dir := directionalScenarios(contract, writes); len(dir) > 0 {
			certified.Scenarios = append(certified.Scenarios, dir...)
			certified.Tests = nil
		}
	}
	// Add a multi-tick SUSTAINED-MOTION floor: a single-tick check ("seed a moving
	// state, run once, x changed") lets a genotype pass while its live multi-tick
	// behavior is degenerate (freezes after one step). The floor re-runs an accepted
	// autonomous-movement case for several steps and asserts the moved field keeps
	// changing — so a cell that stops moving fails, while any correct mover passes.
	if floor := sustainedMotionFloor(certified); floor != nil {
		certified.Scenarios = append(certified.Scenarios, *floor)
	}
	if suiteCount(certified) == 0 {
		return nil
	}
	return certified
}

// directionalScenarios generates BEHAVIORAL checks for a state-writing run-tick
// cell: it must be graded on the state it produces, not its return value. For each
// non-velocity scalar field the cell writes, it seeds the mock world (the
// contract's Init) and asserts the field CHANGES after one tick — a directional
// check a correct mover passes and a do-nothing cell fails. The sustained-motion
// floor then extends the moved fields into a multi-tick trajectory (keeps moving,
// no freeze/jitter). Velocity fields are skipped: they change only on a bounce,
// not every tick, so "changed after one tick" would wrongly fail a correct cell.
func directionalScenarios(c *evolution.AppContract, writes []string) []evolution.Scenario {
	if c == nil || len(writes) == 0 {
		return nil
	}
	init := c.InitSeeds()
	if len(init) == 0 {
		return nil
	}
	byName := map[string]evolution.ContractField{}
	for _, f := range c.Fields {
		byName[strings.ToLower(f.Name)] = f
	}
	var out []evolution.Scenario
	for _, wname := range writes {
		f, ok := byName[strings.ToLower(wname)]
		if !ok || strings.Contains(f.Type, "[") { // scalar contract fields only
			continue
		}
		if isVelocityName(strings.ToLower(f.Name)) {
			continue
		}
		out = append(out, evolution.Scenario{
			Name:  "moves_" + f.Name,
			Entry: "run-tick",
			Steps: 1,
			Seed:  append([]evolution.SeedWrite(nil), init...),
			Expect: evolution.ScenarioExpect{
				Reads: []evolution.SeedWrite{{At: fmt.Sprintf("0x%X", f.Offset), Cmp: "changed"}},
			},
		})
	}
	return out
}

// hmiInputLo/hmiInputHi bound the HMI input register — a seed there means the
// scenario is input-driven, so it is NOT autonomous movement.
const hmiInputLo, hmiInputHi = 0x00050000, 0x00051000

// sustainedMotionFloor derives a multi-tick check from an accepted single-tick
// movement scenario: keep the same seeded state (one the cell already handles
// WITHOUT a collision and WITHOUT input) and run it several steps, asserting the
// field that moved keeps changing. This catches a genotype that passes one tick
// then freezes. Returns nil when the cell shows no autonomous per-tick movement
// (e.g. an input/event cell), so event-driven cells are never forced to move on
// their own. Safe: any cell that genuinely sustains motion passes.
func sustainedMotionFloor(suite *evolution.AcceptanceSuite) *evolution.Scenario {
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
		var traj []evolution.TrajectoryExpect
		for _, r := range sc.Expect.Reads {
			switch r.Cmp {
			case "increased", "decreased", "changed":
				// Require the moved field to visit several DISTINCT values across the
				// run — a whole-path property a genotype can't satisfy by freezing
				// (1 value) or jittering between two, only by genuinely traversing.
				traj = append(traj, evolution.TrajectoryExpect{At: r.At, MinDistinct: 4})
			}
		}
		if len(traj) == 0 {
			continue
		}
		return &evolution.Scenario{
			Name:   "sustains_motion_over_time",
			Seed:   sc.Seed,
			Steps:  8, // 9 samples; MinDistinct 4 tolerates a bounce yet fails jitter
			Entry:  "run-tick",
			Expect: evolution.ScenarioExpect{Trajectory: traj},
		}
	}
	return nil
}

// contractRegion is the persistent sandbox window shared-state contract fields
// live in (EnsureContract filters authored fields into it). A coordination
// `reads` postcondition must land here — that is the cross-cell surface.
const contractRegionLo, contractRegionHi = 0x000B0000, 0x000C0000

// groundScenarios resolves each seed/reads entry's `field` name to the contract
// field's offset and keeps only scenarios that are actually SCORABLE against a
// correct implementation — so a right-behaving cell is never failed by a malformed
// check (the failure mode we hit: draw-position asserts with no seed, coordinates
// outside the window, and a render-frame check on a non-rendering cell).
//
// isUI says whether the cell renders (has a render-frame export). Rules:
//  1. render-frame scenarios promote each `eq` reads-postcondition into a SEED:
//     a renderer writes no shared state, so "assert dot_x==50" only makes sense as
//     "seed dot_x=50, then render" — otherwise the field is 0 and the cell that
//     correctly draws at dot_x fails. This turns a broken assertion into a real one.
//  2. a draw-position (NearX/NearY) assertion must be backed by a seed in the
//     contract region — you cannot verify the sprite is drawn at a position you
//     never put it at. Unseeded position checks are dropped.
//  3. draw coordinates must be inside the window (win_width × win_height, when the
//     contract names them) — an out-of-bounds target (e.g. 350,280 in 300×220) is
//     physically unreachable, so drop it.
//  4. a draw / render-frame scenario on a NON-renderer is dropped: a run-tick
//     physics cell can never satisfy a draw assertion.
func groundScenarios(scs []evolution.Scenario, c *evolution.AppContract, isUI bool) []evolution.Scenario {
	byName := map[string]int{}
	winW, winH := 0, 0
	if c != nil {
		for _, f := range c.Fields {
			lname := strings.ToLower(f.Name)
			byName[lname] = f.Offset
			if strings.Contains(lname, "width") {
				winW = fieldMax(f)
			}
			if strings.Contains(lname, "height") {
				winH = fieldMax(f)
			}
		}
	}
	resolve := func(w *evolution.SeedWrite) {
		name := strings.ToLower(strings.TrimSpace(w.Field))
		if name == "" {
			// tolerate the model putting a field name in `at`
			if _, err := parseOffset(w.At); err != nil {
				name = strings.ToLower(strings.TrimSpace(w.At))
			}
		}
		if off, ok := byName[name]; ok {
			w.At = fmt.Sprintf("0x%X", off)
			w.Field = ""
		}
	}
	// The authored INITIALIZATION (mock world): every scalar field at its Init.
	// Filled in below AFTER a scenario's own seeds and promotions, for the fields it
	// did not itself seed — so a config field (screen_width) or a sibling-produced
	// field the cell reads has a real value instead of zero, while the scenario's
	// own seeds/promotions always win. This is the fix for cells that pass flux_run
	// (which mocks its own inputs) but failed live acceptance (all zeros).
	baseline := c.InitSeeds()
	var out []evolution.Scenario
	for _, sc := range scs {
		// Rule 0 (entry-match, the structural guarantee): force every scenario's entry
		// to the cell's kind. A check must exercise the cell through the SAME export the
		// runtime calls — asserting a state postcondition through render-frame (or a draw
		// through run-tick) grades the wrong function. This is the physics bug's root: a
		// compute cell whose state checks ran via render-frame could never pass.
		if isUI {
			sc.Entry = "render-frame"
		} else if sc.Entry == "render-frame" {
			sc.Entry = "run-tick"
		}
		isRender := sc.Entry == "render-frame"
		// Rule 4: a draw/render assertion only belongs on a rendering cell.
		if !isUI && (isRender || sc.Expect.Draw != nil) {
			continue
		}
		seedsOK := true
		for i := range sc.Seed {
			resolve(&sc.Seed[i]) // resolve field-name seeds (e.g. player_x) to offsets
			if _, err := parseOffset(sc.Seed[i].At); err != nil {
				seedsOK = false // an unresolved field name → empty offset → unscorable
			}
		}
		if !seedsOK {
			continue // a seed didn't resolve; the scenario can't run, so drop it
		}
		// Resolve reads first so promotion/region checks see real offsets.
		readsOK := true
		for i := range sc.Expect.Reads {
			r := &sc.Expect.Reads[i]
			resolve(r)
			off, err := parseOffset(r.At)
			if err != nil || off < contractRegionLo || off >= contractRegionHi {
				readsOK = false // reads an offset outside the contract — invalid coordination check
				break
			}
		}
		if !readsOK {
			continue
		}
		// Rule 1: for a renderer, an exact reads-postcondition is really a seed —
		// the renderer doesn't produce the value, so control it, then assert the draw.
		if isRender {
			for _, r := range sc.Expect.Reads {
				if (r.Cmp == "" || r.Cmp == "eq") && len(r.U32) > 0 {
					if !hasSeedAt(sc.Seed, r.At) {
						sc.Seed = append(sc.Seed, evolution.SeedWrite{At: r.At, U32: r.U32})
					}
				}
			}
		}
		if d := sc.Expect.Draw; d != nil {
			// Rule 3: draw target must be inside the window (when bounds are known).
			if (d.NearX != nil && winW > 0 && (*d.NearX < 0 || *d.NearX > winW)) ||
				(d.NearY != nil && winH > 0 && (*d.NearY < 0 || *d.NearY > winH)) {
				continue
			}
			// Rule 2: a position assertion must be DERIVED FROM SEEDED STATE — the
			// asserted coordinate must equal a value the scenario seeds into a contract
			// field, so a renderer that draws at the field value passes. A NearX/NearY
			// that matches no seed is a magic number the renderer can't be expected to
			// hit (e.g. "window boundary at x=500" in a 300-wide window) — drop it.
			if (d.NearX != nil && !seedHasValue(sc.Seed, uint32(*d.NearX))) ||
				(d.NearY != nil && !seedHasValue(sc.Seed, uint32(*d.NearY))) {
				continue
			}
		}
		// Fill the rest of the mock world: every contract field the scenario did not
		// itself seed (or promote) gets its Init, so the cell reads real config /
		// sibling values instead of zero. The scenario's own seeds are untouched, so
		// position/promotion rules above are unaffected.
		for _, b := range baseline {
			if !hasSeedAt(sc.Seed, b.At) {
				sc.Seed = append(sc.Seed, b)
			}
		}
		// Keep: a draw/result scenario, a reads scenario targeting the contract, or a
		// trajectory scenario (a whole-path assertion on contract state).
		if sc.Expect.Draw != nil || sc.Expect.Result != nil || len(sc.Expect.Reads) > 0 || len(sc.Expect.Trajectory) > 0 {
			out = append(out, sc)
		}
	}
	return out
}

// fieldMax returns the upper bound implied by a contract field's description
// (e.g. "player x, 0..319" → 319), or 0 if none is parseable. Used to bound
// draw-position assertions to the window.
func fieldMax(f evolution.ContractField) int {
	if i := strings.LastIndex(f.Desc, ".."); i >= 0 {
		tail := strings.TrimSpace(f.Desc[i+2:])
		n := 0
		for _, r := range tail {
			if r < '0' || r > '9' {
				break
			}
			n = n*10 + int(r-'0')
		}
		if n > 0 {
			return n
		}
	}
	return 0
}

// hasSeedAt reports whether a seed already writes the given offset string.
func hasSeedAt(seeds []evolution.SeedWrite, at string) bool {
	for _, s := range seeds {
		if s.At == at {
			return true
		}
	}
	return false
}

// seedHasValue reports whether any seed into the shared-state contract region
// writes the value v (in any of its words). A draw-position assertion is only
// scorable if the coordinate it names is a value the scenario actually put into
// shared state — otherwise a correct renderer (which draws at the field value)
// can't be expected to hit it.
func seedHasValue(seeds []evolution.SeedWrite, v uint32) bool {
	for _, s := range seeds {
		off, err := parseOffset(s.At)
		if err != nil || off < contractRegionLo || off >= contractRegionHi {
			continue
		}
		for _, w := range s.U32 {
			if w == v {
				return true
			}
		}
	}
	return false
}

// parseOffset parses a hex/decimal offset string (e.g. "0xB0000").
func parseOffset(s string) (int, error) {
	v, err := strconv.ParseInt(strings.TrimSpace(s), 0, 64)
	return int(v), err
}

// coerceIdentity ensures a proposed subsystem identity is a fresh URN under the
// application namespace, falling back to a generated one when the model's
// suggestion is empty, out-of-namespace, or a duplicate.
func coerceIdentity(id, namespace string, existing []Subsystem) string {
	taken := map[string]bool{}
	for _, s := range existing {
		taken[s.Identity] = true
	}
	if strings.HasPrefix(id, namespace+":") && !taken[id] {
		return id
	}
	return fmt.Sprintf("%s:ext%d", namespace, len(existing)+1)
}

const contractPrompt = `You design the SHARED STATE the subsystems of an application use to coordinate
with each other. Given the objective and the subsystems, output the shared
memory fields — the state that must pass BETWEEN subsystems, not each cell's
private scratch.

FIRST model the DATA STRUCTURE the objective implies, then pick fields:
- A single value (position, score, flag, a config number) is one "i32" field.
- A GRID, IMAGE, BOARD, BUFFER, LIST, or COLLECTION of values is NOT a scalar —
  it is an ARRAY field "i32[N]" whose N is the element COUNT the objective needs.
  Size it explicitly. Examples: a 64x48 pixel/cell grid -> i32[3072]; a 20-cell
  board -> i32[20]; up to ~40 entities each with x,y -> two i32[40] arrays.
  If one subsystem PRODUCES per-cell/per-item data and another CONSUMES it (e.g. a
  calculator fills escape-times and a renderer draws them), that data MUST be a
  shared array — a scalar cannot carry it and the app cannot work without it.

Rules:
- Include every array/grid the objective needs; do not collapse a collection to a
  scalar or omit it. Otherwise include only genuinely shared state.
- Region: offset >= 0x000B0000 and < 0x000C0000, 4-byte aligned. An "i32[N]" field
  occupies N consecutive i32 slots (N*4 bytes); the next field must start after it.
  (Offsets are re-packed for safety, but size arrays correctly.)
- INIT: give every scalar field an "init" — its initial value at boot, forming a
  single COHERENT, LIVE starting world: config fields set (e.g. a screen width
  ~320, height ~240), positions placed INSIDE the window (not 0,0), and at least
  one velocity/speed NON-ZERO so motion actually happens. This is the mock world
  each cell is tested against, so a cell that reads a config or sibling field sees
  a real value, never zero. Omit "init" for arrays.

Output ONLY JSON, no prose or fences:
{"fields":[
  {"name":"ball_x","offset":720896,"type":"i32","desc":"ball x 0..319","init":160},
  {"name":"ball_vx","offset":720900,"type":"i32","desc":"ball x velocity","init":3},
  {"name":"screen_width","offset":720904,"type":"i32","desc":"canvas width","init":320},
  {"name":"escape_times","offset":720912,"type":"i32[3072]","desc":"per-cell escape iterations, row-major"}
]}`

// EnsureContract authors and persists an application's shared-state contract if
// it does not already have one, derived from the envelope's objective and
// subsystems. Idempotent; returns whether it authored a new contract.
func (g *Grower) EnsureContract(ctx context.Context, namespace string) (bool, error) {
	if evolution.LoadContract(g.ledger, namespace) != nil {
		return false, nil
	}
	env := LoadEnvelope(g.ledger, namespace)
	if env == nil {
		return false, nil
	}
	subs := make([]map[string]any, 0, len(env.SubsystemRequirements))
	for _, s := range env.SubsystemRequirements {
		subs = append(subs, map[string]any{
			"identity": s.Identity, "semantics": s.Semantics,
			"reads": s.Reads, "writes": s.Writes,
		})
	}
	user, _ := json.Marshal(map[string]any{"objective": env.Objective, "subsystems": subs})
	resp, err := g.model.InvokeReasoning(ctx, g.prompt("contract", contractPrompt), string(user))
	if err != nil {
		return false, err
	}
	js := extractJSON(resp)
	if js == "" {
		evolution.AddPromptGrievance(g.ledger, "contract", "output contained no JSON shared-state contract object")
		return false, nil
	}
	var c evolution.AppContract
	if json.Unmarshal([]byte(js), &c) != nil {
		evolution.AddPromptGrievance(g.ledger, "contract", "output was not valid JSON matching the {fields:[{name,offset,type,desc}]} schema")
		return false, nil
	}
	// Keep only fields validly placed in the sandbox region.
	var ok []evolution.ContractField
	dropped := 0
	for _, f := range c.Fields {
		if f.Name != "" && f.Offset >= 0xB0000 && f.Offset < 0xC0000 {
			ok = append(ok, f)
		} else {
			dropped++
		}
	}
	if dropped > 0 {
		evolution.AddPromptGrievance(g.ledger, "contract", "placed fields outside the required sandbox region 0xB0000..0xC0000 (they were dropped); all offsets must be within that range")
	}
	// Reconcile: the contract MUST cover every field a component declares it
	// reads/writes, so the ports are always resolvable. Add any declared field the
	// authored contract missed, at the next free aligned offset.
	ok = reconcileDeclaredFields(ok, env)
	if len(ok) == 0 {
		return false, nil
	}
	// Re-pack offsets sequentially so every field — crucially an "i32[N]" array —
	// reserves its full width and no two fields overlap, regardless of the offsets
	// the model chose. Safe: fields are referenced by name (grounded to these
	// offsets) and cells are synthesized after this.
	before := len(ok)
	ok = packOffsets(ok)
	if len(ok) < before {
		log.Printf("[GROW] contract %s: %d field(s) dropped — exceeded the contract region", namespace, before-len(ok))
	}
	c.Fields = ok
	fillInit(&c) // deterministic backstop so the mock world is never broken (bounds set, motion nonzero)
	if err := evolution.SaveContract(g.ledger, namespace, &c); err != nil {
		return false, err
	}
	g.event("create", namespace, fmt.Sprintf("shared-state contract: %d fields", len(ok)))
	return true, nil
}

// fillInit is the deterministic backstop for the authored initialization: it
// guarantees a FUNCTIONAL mock world even if the model gave poor or zero inits —
// screen bounds must be non-zero (else a bounds check collapses), positions start
// inside the window, and at least the velocity/speed fields move (else the sim is
// frozen from boot). It only fills fields the model left at 0; authored non-zero
// inits are respected.
func fillInit(c *evolution.AppContract) {
	if c == nil {
		return
	}
	screenW, screenH := 320, 240
	for i := range c.Fields {
		n := strings.ToLower(c.Fields[i].Name)
		if c.Fields[i].Init == 0 {
			if strings.Contains(n, "width") {
				c.Fields[i].Init = 320
			} else if strings.Contains(n, "height") {
				c.Fields[i].Init = 240
			}
		}
		if strings.Contains(n, "width") && c.Fields[i].Init > 0 {
			screenW = c.Fields[i].Init
		}
		if strings.Contains(n, "height") && c.Fields[i].Init > 0 {
			screenH = c.Fields[i].Init
		}
	}
	for i := range c.Fields {
		f := &c.Fields[i]
		if f.Init != 0 || typeWords(f.Type) != 1 {
			continue
		}
		n := strings.ToLower(f.Name)
		switch {
		case isVelocityName(n):
			f.Init = 2 // small non-zero so motion animates from boot
		case isYName(n):
			f.Init = screenH / 2
		case isXName(n):
			f.Init = screenW / 2
		}
	}
}

func isVelocityName(n string) bool {
	for _, k := range []string{"vel", "vx", "vy", "dx", "dy", "speed", "velocity"} {
		if strings.Contains(n, k) {
			return true
		}
	}
	return false
}
func isXName(n string) bool {
	return strings.HasSuffix(n, "_x") || n == "x" || strings.Contains(n, "pos_x") || strings.Contains(n, "posx")
}
func isYName(n string) bool {
	return strings.HasSuffix(n, "_y") || n == "y" || strings.Contains(n, "pos_y") || strings.Contains(n, "posy")
}

// packOffsets re-addresses contract fields sequentially from the sandbox base so
// each field (including an "i32[N]" array, which spans N consecutive i32 slots)
// reserves its full width and none overlap. A field whose span would spill past the
// contract region is dropped.
func packOffsets(fields []evolution.ContractField) []evolution.ContractField {
	out := make([]evolution.ContractField, 0, len(fields))
	off := 0xB0000
	for _, f := range fields {
		end := off + 4*max(1, typeWords(f.Type))
		if end > 0xC0000 {
			continue
		}
		f.Offset = off
		out = append(out, f)
		off = end
	}
	return out
}

// reconcileDeclaredFields ensures the contract contains every shared field a
// subsystem declares in its reads/writes (except the "HMI input" pseudo-field,
// which is the fixed input register). Missing fields are appended as i32 at the
// next free 4-byte slot, so a declared port never fails to resolve to an offset.
func reconcileDeclaredFields(fields []evolution.ContractField, env *AppEnvelope) []evolution.ContractField {
	have := map[string]bool{}
	next := 0xB0000
	for _, f := range fields {
		have[strings.ToLower(f.Name)] = true
		if end := f.Offset + 4*max(1, typeWords(f.Type)); end > next {
			next = end
		}
	}
	add := func(name string) {
		key := strings.ToLower(strings.TrimSpace(name))
		if key == "" || key == "hmi input" || have[key] {
			return
		}
		have[key] = true
		fields = append(fields, evolution.ContractField{
			Name: strings.TrimSpace(name), Offset: next, Type: "i32",
			Desc: "shared field declared by a component port",
		})
		next += 4
	}
	for _, s := range env.SubsystemRequirements {
		for _, r := range s.Reads {
			add(r)
		}
		for _, w := range s.Writes {
			add(w)
		}
	}
	return fields
}

// typeWords / max: how many i32 slots a contract type occupies.
func typeWords(t string) int {
	i, j := strings.IndexByte(t, '['), strings.IndexByte(t, ']')
	if i < 0 || j <= i+1 {
		return 1
	}
	n, err := strconv.Atoi(strings.TrimSpace(t[i+1 : j]))
	if err != nil || n < 1 {
		return 1
	}
	return n
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func suiteCount(s *evolution.AcceptanceSuite) int {
	if s == nil {
		return 0
	}
	return len(s.Tests) + len(s.Scenarios)
}

// ExpandSuite proposes additional acceptance checks for a cell from its required
// behavior (the descriptor's FunctionalIntent), merges them with the existing
// suite, and runs the whole thing through the adversarial auditor
// (evolution.ValidateSuite) — persisting only the requirement-faithful result.
// It returns the number of net-new certified checks. Bounded by maxSuiteChecks;
// a no-op (0, nil) when the cell has no requirement, is already at the cap, or
// nothing faithful is proposed. On a reasoning fault the stored suite is left
// untouched (fail-open).
func (g *Grower) ExpandSuite(ctx context.Context, cellURN string) (int, error) {
	desc, err := g.repo.Load(cellURN)
	if err != nil {
		return 0, fmt.Errorf("load descriptor: %w", err)
	}
	requirement := strings.TrimSpace(desc.Semantics.FunctionalIntent)
	if requirement == "" {
		return 0, nil
	}

	existing, _ := evolution.LoadAcceptance(g.ledger, cellURN)
	if existing == nil {
		existing = &evolution.AcceptanceSuite{}
	}
	before := suiteCount(existing)
	if before >= maxSuiteChecks {
		return 0, nil
	}

	g.phase("expanding", "proposing new acceptance checks for "+cellURN, cellURN)
	contract := evolution.LoadContract(g.ledger, evolution.AppNamespaceOf(cellURN))
	proposed := g.proposeAdditional(ctx, requirement, kindForCell(g.ledger, cellURN) == KindRender, existing, contract, evolution.AppNamespaceOf(cellURN))
	if suiteCount(proposed) == 0 {
		return 0, nil
	}

	// Adversarially certify the MERGED suite against the requirement; only the
	// faithful checks are kept, so a hallucinated proposal is dropped here.
	merged := mergeSuites(existing, proposed, maxSuiteChecks)
	certified, _, err := evolution.ValidateSuite(ctx, g.model, requirement, merged)
	if err != nil {
		return 0, err // fail-open: never persist unvalidated proposals
	}
	after := suiteCount(certified)
	if after <= before {
		return 0, nil // nothing new survived certification
	}
	if err := evolution.SaveAcceptance(g.ledger, cellURN, certified); err != nil {
		return 0, fmt.Errorf("persist expanded suite: %w", err)
	}
	g.event("create", cellURN, fmt.Sprintf("spec grew %d->%d checks", before, after))
	return after - before, nil
}

// proposeAdditional asks the model for acceptance checks BEYOND those already
// present, given the requirement and the existing checks (so it targets untested
// behavior rather than repeating). Reuses the genesis authoring system prompts.
func (g *Grower) proposeAdditional(ctx context.Context, requirement string, ui bool, existing *evolution.AcceptanceSuite, contract *evolution.AppContract, namespace string) *evolution.AcceptanceSuite {
	user := fmt.Sprintf(`Semantics: %s

%s%s%sThe following checks already exist — propose DIFFERENT, additional ones that
cover behavior not yet tested (harder cases, edge cases, coordination with
sibling cells via the shared-state contract). Prefer checks that VERIFY the plan's
steps for this component. Do NOT repeat any existing check:
%s`, requirement, planContext(g.ledger, namespace), mapContext(g.ledger, namespace), contractContext(contract), summarizeExisting(existing))

	// With a shared-state contract, always author behavioral SCENARIOS (they can
	// seed the input register / shared state and assert on shared memory via
	// "reads" — the only way to test coordination); scalar tests can't.
	sys := acceptancePrompt
	if ui || contract != nil {
		sys = scenarioPrompt
	}
	resp, err := g.model.InvokeReasoning(ctx, sys, user)
	if err != nil {
		return nil
	}
	js := extractJSON(resp)
	if js == "" {
		return nil
	}
	var s evolution.AcceptanceSuite
	if json.Unmarshal([]byte(js), &s) != nil {
		return nil
	}
	// Ground any coordination scenarios in the contract HERE — the shared
	// authoring primitive — so every path (genesis authorCoordination AND the
	// fixpoint ExpandSuite curriculum) resolves `field` names to real offsets and
	// discards reads on invented locations. Only when a contract is in play.
	if contract != nil {
		s.Scenarios = groundScenarios(s.Scenarios, contract, ui)
	}
	return &s
}

// planContext renders the system design plan for an authoring prompt (or "" if
// none), so proposed checks VERIFY the plan's intended behavior — the algorithm
// steps and the choreography that connects components.
func planContext(ledger *storage.LedgerEngine, namespace string) string {
	if namespace == "" {
		return ""
	}
	if p := evolution.LoadPlan(ledger, namespace); p != nil {
		if s := p.RenderSystem(); s != "" {
			return s + "\n"
		}
	}
	return ""
}

// mapContext renders the application map for an authoring prompt (or "" if none),
// so proposed checks are written against the system as it ACTUALLY stands — real
// components, real fields, real verified behavior, and the gaps between them.
func mapContext(ledger *storage.LedgerEngine, namespace string) string {
	if namespace == "" {
		return ""
	}
	m := evolution.LoadAppMap(ledger, namespace)
	if m == nil {
		return ""
	}
	r := m.Render()
	if r == "" {
		return ""
	}
	return r + "\n"
}

// contractContext renders the shared-state contract for the proposer prompt (or
// "" if there is none), so proposed scenarios reference the real shared offsets.
func contractContext(c *evolution.AppContract) string {
	if c == nil {
		return ""
	}
	return c.Render() + "\n"
}

// summarizeExisting renders the current checks as a compact list for the
// proposer's context.
func summarizeExisting(s *evolution.AcceptanceSuite) string {
	if suiteCount(s) == 0 {
		return "(none yet)"
	}
	var b strings.Builder
	for _, t := range s.Tests {
		fmt.Fprintf(&b, "- %s: input %d -> expected %d\n", t.Name, t.Input, t.Expected)
	}
	for _, sc := range s.Scenarios {
		fmt.Fprintf(&b, "- %s (scenario)\n", sc.Name)
	}
	return b.String()
}

// mergeSuites appends add's checks onto base, deduping by name and stopping at
// the cap. base is not mutated.
func mergeSuites(base, add *evolution.AcceptanceSuite, cap int) *evolution.AcceptanceSuite {
	out := &evolution.AcceptanceSuite{
		Tests:     append([]evolution.AcceptanceTest(nil), base.Tests...),
		Scenarios: append([]evolution.Scenario(nil), base.Scenarios...),
	}
	seen := map[string]bool{}
	for _, t := range out.Tests {
		seen[t.Name] = true
	}
	for _, sc := range out.Scenarios {
		seen[sc.Name] = true
	}
	for _, t := range add.Tests {
		if suiteCount(out) >= cap {
			break
		}
		if t.Name == "" || seen[t.Name] {
			continue
		}
		seen[t.Name] = true
		out.Tests = append(out.Tests, t)
	}
	for _, sc := range add.Scenarios {
		if suiteCount(out) >= cap {
			break
		}
		if sc.Name == "" || seen[sc.Name] {
			continue
		}
		seen[sc.Name] = true
		out.Scenarios = append(out.Scenarios, sc)
	}
	return out
}

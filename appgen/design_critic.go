package appgen

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/mdcfrancis/flow/evolution"
)

// The DESIGN critics are adversarial reviewers of the DESIGN artifacts (the contract
// and the plan), run BEFORE and independently of code synthesis — the counterpart to
// the code/vision critics, which judge OUTPUTS after the fact. A design flaw (a
// continuous quantity typed i32, a map leaf planned as a renderer) otherwise surfaces
// only as repeated synthesis failures: the cell churns for hours because the fault is
// upstream of the code. A categorical critic catches it once, on the design, and
// REPAIRS the artifact so every cell is synthesized from a corrected design.
//
// Two categories today:
//   - the TYPE critic repairs the CONTRACT (a field's declared type must cohere with
//     the operations the plan performs on it, and be agreed by all its readers/writers);
//   - the ARCHITECTURE critic repairs the PLAN (structural coherence: every field has a
//     reader and a writer, a map leaf computes an element rather than drawing, the
//     choreography is consistent with the data dependencies).
//
// Each repair is applied by rewriting the persisted artifact through the ledger, so it
// is versioned and reversible like any other block.

// DesignRepair is one change a design critic made to a design artifact — surfaced to the
// operator (and the activity log) so the system's self-revision is legible.
type DesignRepair struct {
	Category string // "type" | "architecture"
	Target   string // contract field name, or component identity
	Before   string
	After    string
	Reason   string
}

func (r DesignRepair) String() string {
	if r.Before != "" && r.After != "" {
		return fmt.Sprintf("[%s] %s: %s → %s — %s", r.Category, shortTarget(r.Target), r.Before, r.After, r.Reason)
	}
	return fmt.Sprintf("[%s] %s — %s", r.Category, shortTarget(r.Target), r.Reason)
}

func shortTarget(t string) string {
	if i := strings.LastIndexByte(t, ':'); i >= 0 && strings.HasPrefix(t, "urn:") {
		return t[i+1:]
	}
	return t
}

// CritiqueDesign runs every categorical design critic over an application's design
// artifacts and APPLIES the repairs they find, returning the changes made (empty if the
// design is coherent). Best-effort and fail-open: a model or ledger fault leaves the
// design untouched (synthesis proceeds from today's design). The caller throttles it and
// de-duplicates on the design fingerprint so it only re-runs when the design changed.
func (g *Grower) CritiqueDesign(ctx context.Context, namespace string) ([]DesignRepair, error) {
	if g.model == nil {
		return nil, nil
	}
	// GENERATOR PATH: when a canonical model exists it is the single source of truth, so
	// coherence is ONE question — is the model sound? The critic reasons from and repairs
	// the MODEL; the contract and ports are then re-projected/re-resolved, so type and
	// wiring drift cannot survive. (The separate type+architecture critics below are the
	// legacy path for apps grown before the model layer.)
	if evolution.LoadModel(g.ledger, namespace) != nil {
		return g.critiqueModel(ctx, namespace)
	}
	var all []DesignRepair
	typeReps, err := g.critiqueTypes(ctx, namespace)
	if err != nil {
		return all, err
	}
	all = append(all, typeReps...)
	archReps, err := g.critiqueArchitecture(ctx, namespace)
	if err != nil {
		return all, err
	}
	all = append(all, archReps...)
	return all, nil
}

// ---------------------------------------------------------------------------
// Model-conformance critic — repairs the MODEL (the source of truth).
// ---------------------------------------------------------------------------

const modelCriticPrompt = `You are a STRICT, ADVERSARIAL reviewer of an application's CANONICAL MODEL — the single
source of truth its contract and code derive from. You are given the OBJECTIVE and the MODEL
(entities with a cardinality and typed fields, plus the per-tick dynamics). Find every way
the model fails to cohere, and output precise CHANGES. Try to REFUTE that the model is
sound; do not rubber-stamp.

Check:
- TYPE: each field's element type must fit how the DYNAMICS use it. A CONTINUOUS quantity —
  a position, velocity, acceleration, force, angle, or anything integrated over time,
  divided, or accumulated with fractional deltas — MUST be f32 (integer math floors to
  zero, so it silently dies as i32). An integer — a count, index, flag, RGBA color, or pixel
  dimension — is i32.
- DEAD STATE: a field that no dynamic step reads or writes and the objective does not need
  is dead — remove it.
- MISSING STATE: a quantity the dynamics or the objective require that no entity carries —
  add it to the right entity with the right type.
- Do NOT rename fields or add an entity-name prefix (write "x", not "particle_x") — the
  projection handles naming. Do not restate a coherent field.

Output ONLY JSON, no prose or fences — an empty array if the model is sound:
{"changes":[
  {"kind":"retype","entity":"<entity>","field":"<field>","to":"f32|i32","reason":"<grounded in the dynamics>"},
  {"kind":"add_field","entity":"<entity>","field":"<field>","type":"f32|i32","reason":"..."},
  {"kind":"remove_field","entity":"<entity>","field":"<field>","reason":"..."}
]}`

func (g *Grower) critiqueModel(ctx context.Context, namespace string) ([]DesignRepair, error) {
	m := evolution.LoadModel(g.ledger, namespace)
	if m == nil {
		return nil, nil
	}
	user, _ := json.Marshal(map[string]any{"objective": m.Objective, "model": m.Render()})
	g.phase("critique", "checking the model of "+namespace+" for coherence", namespace)
	resp, err := g.model.InvokeReasoning(ctx, g.prompt("model-critic", modelCriticPrompt), string(user))
	if err != nil {
		return nil, err
	}
	js := extractJSON(resp)
	if js == "" {
		return nil, nil
	}
	var out struct {
		Changes []struct {
			Kind, Entity, Field, Type, To, Reason string
		} `json:"changes"`
	}
	if json.Unmarshal([]byte(js), &out) != nil {
		evolution.AddPromptGrievance(g.ledger, "model-critic", "output was not the {changes:[{kind,entity,field,type|to,reason}]} JSON schema")
		return nil, nil
	}

	var reps []DesignRepair
	changed := false
	for _, ch := range out.Changes {
		ent := findEntity(m, ch.Entity)
		if ent == nil {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(ch.Kind)) {
		case "retype":
			to := projElem(ch.To)
			for i := range ent.Fields {
				if strings.EqualFold(ent.Fields[i].Name, ch.Field) && !strings.EqualFold(ent.Fields[i].Type, to) {
					reps = append(reps, DesignRepair{Category: "model", Target: ent.Name + "." + ent.Fields[i].Name,
						Before: ent.Fields[i].Type, After: to, Reason: strings.TrimSpace(ch.Reason)})
					ent.Fields[i].Type = to
					changed = true
				}
			}
		case "add_field":
			if strings.TrimSpace(ch.Field) == "" || fieldOf(ent, ch.Field) != nil {
				continue
			}
			ent.Fields = append(ent.Fields, evolution.EntityField{Name: strings.TrimSpace(ch.Field), Type: projElem(ch.Type), Desc: strings.TrimSpace(ch.Reason)})
			reps = append(reps, DesignRepair{Category: "model", Target: ent.Name + "." + ch.Field, After: projElem(ch.Type), Reason: "added: " + strings.TrimSpace(ch.Reason)})
			changed = true
		case "remove_field":
			if f := fieldOf(ent, ch.Field); f != nil {
				dropField(ent, ch.Field)
				reps = append(reps, DesignRepair{Category: "model", Target: ent.Name + "." + ch.Field, Before: f.Type, Reason: "removed (dead): " + strings.TrimSpace(ch.Reason)})
				changed = true
			}
		}
	}
	if !changed {
		return nil, nil
	}
	if err := evolution.SaveModel(g.ledger, namespace, m); err != nil {
		return reps, err
	}
	// Re-project the contract and re-resolve ports from the repaired model, so the whole
	// design follows the correction — this is the point of a single source of truth.
	if _, err := g.projectContract(namespace); err != nil {
		return reps, err
	}
	g.resolvePortsToModel(namespace)
	for _, r := range reps {
		g.event("mutate", namespace, "model critic: "+r.String())
	}
	return reps, nil
}

func findEntity(m *evolution.SystemModel, name string) *evolution.Entity {
	for i := range m.Entities {
		if strings.EqualFold(m.Entities[i].Name, name) {
			return &m.Entities[i]
		}
	}
	return nil
}

func fieldOf(e *evolution.Entity, name string) *evolution.EntityField {
	for i := range e.Fields {
		if strings.EqualFold(e.Fields[i].Name, name) {
			return &e.Fields[i]
		}
	}
	return nil
}

func dropField(e *evolution.Entity, name string) {
	out := e.Fields[:0]
	for _, f := range e.Fields {
		if !strings.EqualFold(f.Name, name) {
			out = append(out, f)
		}
	}
	e.Fields = out
}

// projElem clamps a critic-proposed type to the two element types the substrate addresses.
func projElem(t string) string {
	if strings.EqualFold(strings.TrimSpace(t), "f32") {
		return "f32"
	}
	return "i32"
}

// ---------------------------------------------------------------------------
// Type critic — repairs the CONTRACT.
// ---------------------------------------------------------------------------

const typeCriticPrompt = `You are a STRICT, ADVERSARIAL TYPE reviewer for a small application. You are given the
OBJECTIVE, the SHARED-STATE CONTRACT (each field: name, current type, description), and the
PLAN (how each component uses those fields). Your ONE job: find every field whose DECLARED
type is wrong for how it is actually used, and propose the correct type. Actively try to
find a mistype; do not rubber-stamp.

The type system is deliberately small:
- i32       — integers: an index, a count, a loop bound, an id, a bitmask/flag, an RGBA
              color, or an EXACT pixel dimension.
- f32       — a CONTINUOUS quantity: a position, velocity, acceleration, force, angle,
              mass, or anything integrated over time, accumulated with fractional deltas,
              divided, or scaled by a sub-unit factor. Integer division FLOORS TO ZERO, so
              a velocity or gravity kept in i32 silently vanishes — that is the classic bug.
- i32[N] / f32[N]  — a fixed-size ARRAY of the above (N elements).

Rules:
- Judge from the OBJECTIVE and the PLAN's steps, not the current type. If the plan says a
  field is integrated, divided, or given a fractional/gravitational update, it MUST be f32.
- An ARRAY keeps its arity: propose f32[256] for an i32[256] of particle positions — NEVER
  drop the [N], never change N.
- Every field has ONE type. If two components read/write a field with conflicting
  assumptions, pick the type the DOMINANT (continuous) use requires.
- Only propose a change you can justify from the described behavior. If a field's type is
  already correct, do not list it.

Output ONLY JSON, no prose or fences — an empty array if every type is already correct:
{"changes":[{"field":"<name>","from":"<current type>","to":"<correct type>","reason":"<why, grounded in the plan's use of this field>"}]}`

var contractTypeRE = regexp.MustCompile(`^(i32|f32)(\[[0-9]+\])?$`)

// arrayArity returns the "[N]" suffix of a contract type, or "" for a scalar.
func arrayArity(t string) string {
	if i := strings.IndexByte(t, '['); i >= 0 {
		return t[i:]
	}
	return ""
}

func (g *Grower) critiqueTypes(ctx context.Context, namespace string) ([]DesignRepair, error) {
	contract := evolution.LoadContract(g.ledger, namespace)
	if contract == nil || len(contract.Fields) == 0 {
		return nil, nil
	}
	env := LoadEnvelope(g.ledger, namespace)
	if env == nil {
		return nil, nil
	}
	plan := evolution.LoadPlan(g.ledger, namespace)
	// Only the SYSTEM design (overview + choreography + component purposes) is needed to
	// judge types — the choreography describes how each field is updated (integrated,
	// divided, accumulated), which is what decides f32 vs i32. Sending every component's
	// full step list would bloat the prompt and time out the slow local reasoner for no
	// gain; the architecture critic is the one that needs per-component detail.
	planText := ""
	if plan != nil {
		planText = plan.RenderSystem()
	}
	fields := make([]map[string]string, 0, len(contract.Fields))
	current := map[string]string{}
	for _, f := range contract.Fields {
		current[f.Name] = strings.TrimSpace(f.Type)
		fields = append(fields, map[string]string{"name": f.Name, "type": f.Type, "desc": f.Desc})
	}
	user, _ := json.Marshal(map[string]any{
		"objective": env.Objective, "contract_fields": fields, "plan": planText,
	})
	g.phase("critique", "type-checking the design of "+namespace, namespace)
	resp, err := g.model.InvokeReasoning(ctx, g.prompt("type-critic", typeCriticPrompt), string(user))
	if err != nil {
		return nil, err
	}
	js := extractJSON(resp)
	if js == "" {
		return nil, nil
	}
	var out struct {
		Changes []struct {
			Field, From, To, Reason string
		} `json:"changes"`
	}
	if json.Unmarshal([]byte(js), &out) != nil {
		evolution.AddPromptGrievance(g.ledger, "type-critic", "output was not the {changes:[{field,from,to,reason}]} JSON schema")
		return nil, nil
	}

	var reps []DesignRepair
	changed := false
	for _, ch := range out.Changes {
		cur, ok := current[ch.Field]
		if !ok {
			// Named a field that isn't in the contract — malformed; teach the prompt.
			evolution.AddPromptGrievance(g.ledger, "type-critic", "proposed a type change for a field that is not in the contract (only name contract fields)")
			continue
		}
		to := strings.TrimSpace(ch.To)
		if to == cur || !contractTypeRE.MatchString(to) {
			continue // no-op or not a representable type — reject
		}
		if arrayArity(cur) != arrayArity(to) {
			// Must preserve array arity (i32[256]→f32[256], never →f32 or a different N).
			evolution.AddPromptGrievance(g.ledger, "type-critic", "a type change must keep the array arity: i32[N]→f32[N], never drop or resize [N]")
			continue
		}
		for i := range contract.Fields {
			if contract.Fields[i].Name == ch.Field {
				contract.Fields[i].Type = to
				changed = true
				reps = append(reps, DesignRepair{Category: "type", Target: ch.Field, Before: cur, After: to, Reason: strings.TrimSpace(ch.Reason)})
				current[ch.Field] = to
				break
			}
		}
	}
	if changed {
		if err := evolution.SaveContract(g.ledger, namespace, contract); err != nil {
			return nil, err
		}
		for _, r := range reps {
			g.event("mutate", namespace, "type critic: "+r.String())
		}
	}
	return reps, nil
}

// ---------------------------------------------------------------------------
// Architecture critic — repairs the PLAN.
// ---------------------------------------------------------------------------

const archCriticPrompt = `You are a STRICT, ADVERSARIAL SOFTWARE ARCHITECT reviewing the DESIGN PLAN of a small
application. You are given the OBJECTIVE, the SHARED-STATE CONTRACT, and the current PLAN
(an overview, the per-tick/frame choreography, and each component's purpose, entry point,
the shared fields it reads/writes, its algorithm steps, and its interactions). Find every
STRUCTURAL flaw and produce a corrected design for the affected components. Try to REFUTE
that the design is sound; do not give the benefit of the doubt.

Look for:
- A field a component WRITES that no component READS (a dead output), or a field a
  component READS that no component WRITES (a dangling input) — the wiring is broken.
- TWO fields that hold the SAME state under different names (e.g. a component maps over
  "particle_states" while the real data lives in particle_x/particle_y/particle_vx, or an
  "attractor_pos" duplicates attractor_x/attractor_y). Point the affected component's steps
  and interactions at the REAL fields, and record the duplication so the wiring is honest.
- A component's ENTRY is wrong for its job: a map/iteration LEAF (it processes ONE element
  and returns the updated element) must be a "run-tick" compute step, NOT a "render-frame"
  renderer. A component that DRAWS must be "render-frame"; one that only mutates state must
  be "run-tick". Flag any plan whose steps contradict its entry.
- Two components own (write) the same field with no coordinating order — a race.
- A choreography step names a component that does not exist, or orders a consumer before
  its producer.
- A component's steps do not actually produce the outputs it declares (or read the inputs
  it needs).

For each affected component, REWRITE its plan (purpose/steps/interactions/invariants) so the
structure is sound — keep it a direct, implementable design that names the exact shared
fields. Do NOT invent new components or new shared fields; work within the given contract
and roster. Output ONLY JSON, no prose or fences — empty findings if the design is sound:
{"findings":[
  {"component":"<identity>","problem":"<the structural flaw>",
   "revised":{"purpose":"<one sentence>","steps":["..."],"interactions":["..."],"invariants":["..."]}}
],
 "choreography":["<revised ordered step>", "..."]}
Omit "choreography" (or leave it empty) unless the ordering itself was wrong.`

func (g *Grower) critiqueArchitecture(ctx context.Context, namespace string) ([]DesignRepair, error) {
	plan := evolution.LoadPlan(g.ledger, namespace)
	if plan == nil || len(plan.Components) == 0 {
		return nil, nil // nothing designed yet — AuthorPlan runs first
	}
	env := LoadEnvelope(g.ledger, namespace)
	if env == nil {
		return nil, nil
	}
	contract := evolution.LoadContract(g.ledger, namespace)

	var planText strings.Builder
	planText.WriteString(plan.RenderSystem())
	for i := range plan.Components {
		planText.WriteString("\n")
		planText.WriteString(plan.Components[i].Render())
	}
	user, _ := json.Marshal(map[string]any{
		"objective": env.Objective, "shared_state": contractText(contract), "plan": planText.String(),
	})
	g.phase("critique", "auditing the architecture of "+namespace, namespace)
	resp, err := g.model.InvokeReasoning(ctx, g.prompt("architecture-critic", archCriticPrompt), string(user))
	if err != nil {
		return nil, err
	}
	js := extractJSON(resp)
	if js == "" {
		return nil, nil
	}
	var out struct {
		Findings []struct {
			Component string `json:"component"`
			Problem   string `json:"problem"`
			Revised   struct {
				Purpose      string   `json:"purpose"`
				Steps        []string `json:"steps"`
				Interactions []string `json:"interactions"`
				Invariants   []string `json:"invariants"`
			} `json:"revised"`
		} `json:"findings"`
		Choreography []string `json:"choreography"`
	}
	if json.Unmarshal([]byte(js), &out) != nil {
		evolution.AddPromptGrievance(g.ledger, "architecture-critic", "output was not the {findings:[{component,problem,revised}],choreography} JSON schema")
		return nil, nil
	}

	// Ground truth: a repair may change a component's DESIGN (purpose/steps/interactions/
	// invariants) and the choreography, but never its ports or entry — those come from the
	// envelope. Re-anchor after applying so the critic cannot silently rewrite the wiring.
	valid := map[string]bool{}
	for _, s := range env.SubsystemRequirements {
		valid[s.Identity] = true
	}
	var reps []DesignRepair
	changed := false
	for _, f := range out.Findings {
		if !valid[f.Component] || strings.TrimSpace(f.Problem) == "" {
			if !valid[f.Component] {
				evolution.AddPromptGrievance(g.ledger, "architecture-critic", "named a component that is not in this application's roster")
			}
			continue
		}
		cp := plan.Component(f.Component)
		if cp == nil {
			continue
		}
		rev := *cp
		if p := strings.TrimSpace(f.Revised.Purpose); p != "" {
			rev.Purpose = p
		}
		if len(f.Revised.Steps) > 0 {
			rev.Steps = f.Revised.Steps
		}
		if len(f.Revised.Interactions) > 0 {
			rev.Interactions = f.Revised.Interactions
		}
		if len(f.Revised.Invariants) > 0 {
			rev.Invariants = f.Revised.Invariants
		}
		rev.Notes = appendNote(cp.Notes, "architecture: "+f.Problem)
		plan.Upsert(rev)
		changed = true
		reps = append(reps, DesignRepair{Category: "architecture", Target: f.Component, Reason: f.Problem})
	}
	if len(out.Choreography) > 0 {
		plan.Choreography = out.Choreography
		changed = true
	}
	if changed {
		groundPlanPorts(plan, env) // ports + entry stay ground-truth from the envelope
		if err := evolution.SavePlan(g.ledger, namespace, plan); err != nil {
			return nil, err
		}
		for _, r := range reps {
			g.event("mutate", namespace, "architecture critic: "+r.String())
		}
	}
	return reps, nil
}

// appendNote adds a one-line refinement reason to a plan's accumulated Notes, bounding
// the history so it cannot grow without limit.
func appendNote(existing, note string) string {
	note = strings.TrimSpace(note)
	if note == "" {
		return existing
	}
	if existing == "" {
		return note
	}
	joined := existing + "; " + note
	const max = 600
	if len(joined) > max {
		joined = joined[len(joined)-max:]
	}
	return joined
}

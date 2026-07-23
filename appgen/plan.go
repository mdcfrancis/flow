package appgen

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mdcfrancis/flow/evolution"
)

// planPrompt authors the whole-application design: an overview, the per-tick/frame
// choreography that connects the components, and each component's step-by-step
// algorithm and interactions. The shared fields and each component's read/write
// ports are GIVEN (from the envelope + contract); the model designs the behavior.
const planPrompt = `You are a software architect writing the DESIGN PLAN for a small application that
other engineers will implement one component (cell) at a time. You are given the
OBJECTIVE, the SHARED-STATE fields the components coordinate through, and the
COMPONENTS with the exact fields each reads and writes.

Produce a concrete, implementable design — the code will be a direct
implementation of it. Output ONLY JSON, no prose or fences:
{
  "overview": "<2-3 sentences: the approach and how state flows through the system>",
  "choreography": ["<what happens first each tick/frame>", "<then this>", "..."],
  "components": [
    { "identity": "<component identity>",
      "purpose": "<one sentence: its job in the system>",
      "steps": ["<ordered algorithm step referencing the exact shared fields>", "..."],
      "interactions": ["<which sibling it hands off to / receives from, via which field>"],
      "invariants": ["<a condition that must always hold: a bound, a flag, an edge case>"] }
  ]
}
Rules:
- Every step must be concrete enough to implement directly (name the exact shared
  field, the condition, and the update — e.g. "if the HMI keydown is 'd' (68),
  add the step to player_x, then clamp player_x to [0, 300]").
- Wire the components together: a field one writes, another reads. Say so in the
  interactions.
- A VIEW/render component READS the dynamic state fields and draws each entity AT
  the value it reads — never a fixed position.
- A component that COLORS cells/pixels BY a scalar value (a heatmap/fractal/gradient)
  must map the value to a HIGH-CONTRAST color so the structure is visible: a sentinel
  or extreme value (e.g. "in the set" = the maximum iteration count) is BLACK/dark,
  and other values sweep a BRIGHT gradient (brightness or hue rising with the value).
  Normalize across the value range and span dark→bright — never a near-uniform fill,
  and never ignore the value.
- Design only what the objective needs; no speculative features.`

// componentPlanPrompt designs ONE component against the existing system plan —
// used when a new component appears (a fracture child, or an architecture
// addition) so it is designed to fit what already exists.
const componentPlanPrompt = `You design ONE component of an existing application, to fit the system already
designed around it. You are given the OBJECTIVE, the SYSTEM PLAN so far, the
SHARED-STATE fields, and THIS component's role and the exact fields it reads and
writes. Output ONLY JSON, no prose or fences:
{ "purpose": "<one sentence>",
  "steps": ["<ordered, directly-implementable algorithm step naming exact fields>"],
  "interactions": ["<handoff to/from a sibling via a shared field>"],
  "invariants": ["<a condition that must always hold>"] }
Every step must name the exact shared field and the concrete update. A view/render
component reads the state and draws entities AT the values it reads. A component that
COLORS by a scalar value must map it to a HIGH-CONTRAST color — a sentinel/extreme
value (e.g. max iterations = "in the set") is BLACK, others sweep a bright gradient —
spanning dark→bright so the structure is visible, never a near-uniform fill.`

// AuthorPlan writes the application's design plan up front, from the objective +
// the declared component interfaces + the contract, and persists it. The model
// designs behavior; the reads/writes are ground truth from the envelope ports, so
// the plan can never drift from the contract. Best-effort: a fault leaves no plan
// (synthesis simply falls back to today's thin spec).
func (g *Grower) AuthorPlan(ctx context.Context, namespace string) error {
	env := LoadEnvelope(g.ledger, namespace)
	if env == nil || strings.TrimSpace(env.Objective) == "" {
		return nil
	}
	if evolution.LoadPlan(g.ledger, namespace) != nil {
		return nil // authored once; RefreshPlan keeps it current
	}
	contract := evolution.LoadContract(g.ledger, namespace)

	comps := make([]map[string]any, 0, len(env.SubsystemRequirements))
	for _, s := range env.SubsystemRequirements {
		comps = append(comps, map[string]any{
			"identity": s.Identity, "role": s.Semantics,
			"reads": s.Reads, "writes": s.Writes,
		})
	}
	user, _ := json.Marshal(map[string]any{
		"objective": env.Objective, "shared_state": contractText(contract), "components": comps,
	})
	g.phase("planning", "designing the application plan for "+namespace, namespace)
	resp, err := g.model.InvokeReasoning(ctx, g.prompt("plan", planPrompt), string(user))
	if err != nil {
		return err
	}
	js := extractJSON(resp)
	if js == "" {
		evolution.AddPromptGrievance(g.ledger, "plan", "output contained no JSON design-plan object")
		return nil
	}
	var p evolution.AppPlan
	if json.Unmarshal([]byte(js), &p) != nil {
		evolution.AddPromptGrievance(g.ledger, "plan", "output was not valid JSON matching the {overview, choreography[], components[{identity,purpose,steps,interactions,invariants}]} schema")
		return nil
	}
	p.Namespace = namespace
	p.Objective = env.Objective
	groundPlanPorts(&p, env) // reads/writes + entry come from the envelope, not the model
	if err := evolution.SavePlan(g.ledger, namespace, &p); err != nil {
		return err
	}
	g.event("create", namespace, fmt.Sprintf("application plan: %d components", len(p.Components)))
	return nil
}

// RefreshPlan keeps the plan current as the architecture changes: it re-anchors
// each component's ports to the envelope, drops components that were retired (e.g.
// a fractured parent), and DESIGNS a plan for any new component (a fracture child
// or an architecture addition) so it fits the system already planned. Bounded: a
// model call happens only for components that lack a plan.
func (g *Grower) RefreshPlan(ctx context.Context, namespace string) error {
	env := LoadEnvelope(g.ledger, namespace)
	if env == nil {
		return nil
	}
	p := evolution.LoadPlan(g.ledger, namespace)
	if p == nil {
		return g.AuthorPlan(ctx, namespace) // no plan yet — author the whole thing
	}
	p.Objective = env.Objective
	contract := evolution.LoadContract(g.ledger, namespace)

	live := map[string]bool{}
	for _, sub := range env.SubsystemRequirements {
		live[sub.Identity] = true
		if cp := p.Component(sub.Identity); cp != nil {
			cp.Reads, cp.Writes, cp.Entry = sub.Reads, sub.Writes, entryFor(sub)
			if cp.Purpose == "" {
				cp.Purpose = sub.Semantics
			}
			p.Upsert(*cp)
			continue
		}
		// New component — design it against the existing system plan.
		np := g.authorComponentPlan(ctx, p, contract, sub)
		p.Upsert(np)
	}
	for _, c := range append([]evolution.ComponentPlan(nil), p.Components...) {
		if !live[c.Identity] {
			p.Remove(c.Identity)
		}
	}
	return evolution.SavePlan(g.ledger, namespace, p)
}

// authorComponentPlan designs a single component to fit the current system plan.
// On any fault it returns a minimal plan (ports + role) so the component still has
// a design skeleton.
func (g *Grower) authorComponentPlan(ctx context.Context, p *evolution.AppPlan, contract *evolution.AppContract, sub Subsystem) evolution.ComponentPlan {
	base := evolution.ComponentPlan{
		Identity: sub.Identity, Purpose: sub.Semantics, Entry: entryFor(sub),
		Reads: sub.Reads, Writes: sub.Writes,
	}
	if g.model == nil {
		return base
	}
	user, _ := json.Marshal(map[string]any{
		"objective":    p.Objective,
		"system_plan":  p.RenderSystem(),
		"shared_state": contractText(contract),
		"component": map[string]any{
			"identity": sub.Identity, "role": sub.Semantics,
			"reads": sub.Reads, "writes": sub.Writes,
		},
	})
	resp, err := g.model.InvokeReasoning(ctx, g.prompt("component-plan", componentPlanPrompt), string(user))
	if err != nil {
		return base
	}
	js := extractJSON(resp)
	if js == "" {
		return base
	}
	var got evolution.ComponentPlan
	if json.Unmarshal([]byte(js), &got) != nil {
		return base
	}
	base.Purpose = firstNonEmpty(got.Purpose, base.Purpose)
	base.Steps, base.Interactions, base.Invariants = got.Steps, got.Interactions, got.Invariants
	base.Notes = "designed on architecture change"
	return base
}

// groundPlanPorts overrides each component plan's reads/writes/entry with the
// envelope's declared ports (ground truth), and fills purpose from semantics when
// the model left it blank.
func groundPlanPorts(p *evolution.AppPlan, env *AppEnvelope) {
	byID := map[string]Subsystem{}
	for _, s := range env.SubsystemRequirements {
		byID[s.Identity] = s
	}
	for i := range p.Components {
		if s, ok := byID[p.Components[i].Identity]; ok {
			p.Components[i].Reads = s.Reads
			p.Components[i].Writes = s.Writes
			p.Components[i].Entry = entryFor(s)
			if p.Components[i].Purpose == "" {
				p.Components[i].Purpose = s.Semantics
			}
		}
	}
}

// contractText renders the contract for a plan prompt, or "" if none.
func contractText(c *evolution.AppContract) string {
	if c == nil {
		return ""
	}
	return c.Render()
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

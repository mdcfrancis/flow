package appgen

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mdcfrancis/flow/evolution"
)

// modelPrompt authors the CANONICAL MODEL — the single source of truth the contract and
// ports derive from. It names the entities (nouns), their cardinality and typed state, and
// the per-tick dynamics, in ONE coherent vocabulary, so nothing downstream has to reinvent
// (and drift) the names or types.
const modelPrompt = `You are a software architect defining the CANONICAL MODEL of a small application — the
single source of truth from which the shared-state contract and every component's ports are
DERIVED. Given the OBJECTIVE and the COMPONENTS (each with a role), produce the model.

Identify the ENTITIES — the nouns the system is about (e.g. a particle, an attractor, the
screen). For each entity give:
- "cardinality": how many INSTANCES exist at once — 1 for a singleton, or a fixed count N
  for a collection (e.g. 100 particles). A collection's fields become arrays automatically.
- "fields": its STATE, each with an ELEMENT type:
    * "f32" for a CONTINUOUS quantity — a position, velocity, acceleration, force, angle, or
      anything integrated over time, divided, or accumulated with fractional deltas. Integer
      math FLOORS to zero, so continuous state kept as i32 silently dies. Use f32.
    * "i32" for an integer — a count, an index, a flag, an RGBA color, or a pixel dimension.
Then give the DYNAMICS: the ordered per-tick transformations, in terms of the entities and
their fields.

Finally, design the INITIAL CONDITIONS — the state the app BOOTS from, before any tick runs.
This is DESIGN, not a per-tick rule: without it a collection's arrays boot at zero and every
instance is stacked on one point (a particle system needs its instances SPREAD OUT). For each
field that needs a non-zero or non-degenerate start, give a distribution:
  - "uniform" over [min,max] — each instance a different value in the range (use this to
    SCATTER a collection's positions across the screen: x uniform [0, screen width], y uniform
    [0, screen height]; and for a lively start, a small random velocity, e.g. [-1, 1]);
  - "spread" over [min,max] — evenly stepped by index (a ramp);
  - "const" value — every instance the same (e.g. an attractor at the screen center).
Scatter EVERY collection's positions — that is the difference between a visible field and a
single dot.

Rules:
- Name fields WITHOUT the entity prefix: write "x", "vx" — NOT "particle_x". The projection
  adds "<entity>_" for you. One name per quantity; do not introduce two names for one thing.
- Model exactly what the OBJECTIVE needs — no speculative entities or fields.
- Use the screen dimensions you defined for the scatter ranges (e.g. a 320×240 screen).
- If ARCHITECTURAL GUIDANCE is given, honor it.

Output ONLY JSON, no prose or fences:
{"entities":[{"name":"particle","cardinality":100,"purpose":"<one line>",
  "fields":[{"name":"x","type":"f32","desc":"<one line>"}]}],
 "dynamics":["<ordered per-tick transformation naming entities and fields>"],
 "init":[{"entity":"particle","field":"x","dist":"uniform","min":0,"max":320},
         {"entity":"particle","field":"y","dist":"uniform","min":0,"max":240},
         {"entity":"particle","field":"vx","dist":"uniform","min":-1,"max":1},
         {"entity":"attractor","field":"x","dist":"const","value":160}]}`

// AuthorModel writes the application's canonical system model up front, from the objective
// and the declared components' roles, and persists it at <ns>:model. It is the ROOT
// artifact: EnsureContract projects the contract from it and the ports resolve to it.
// Authored once; the conformance critic keeps it current. Best-effort — a fault leaves no
// model and EnsureContract falls back to authoring the contract directly (old path).
func (g *Grower) AuthorModel(ctx context.Context, namespace string) error {
	if g.model == nil {
		return nil
	}
	env := LoadEnvelope(g.ledger, namespace)
	if env == nil || strings.TrimSpace(env.Objective) == "" {
		return nil
	}
	if evolution.LoadModel(g.ledger, namespace) != nil {
		return nil // authored once
	}
	comps := make([]map[string]any, 0, len(env.SubsystemRequirements))
	for _, s := range env.SubsystemRequirements {
		comps = append(comps, map[string]any{"identity": s.Identity, "role": s.Semantics})
	}
	user, _ := json.Marshal(map[string]any{
		"objective": env.Objective, "components": comps,
		"architectural_guidance": g.planKnowledge(planIntent(env), 3),
	})
	g.phase("modeling", "defining the canonical model for "+namespace, namespace)
	resp, err := g.model.InvokeReasoning(ctx, g.prompt("model", modelPrompt), string(user))
	if err != nil {
		return err
	}
	js := extractJSON(resp)
	if js == "" {
		evolution.AddPromptGrievance(g.ledger, "model", "output contained no JSON model object")
		return nil
	}
	var m evolution.SystemModel
	if json.Unmarshal([]byte(js), &m) != nil || len(m.Entities) == 0 {
		evolution.AddPromptGrievance(g.ledger, "model", "output was not valid JSON matching the {entities:[{name,cardinality,fields:[{name,type}]}],dynamics:[]} schema")
		return nil
	}
	m.Namespace = namespace
	m.Objective = env.Objective
	if filled := ensureCollectionInit(&m); len(filled) > 0 {
		g.event("create", namespace, "backfilled scatter initial conditions for "+strings.Join(filled, ", "))
	}
	if err := evolution.SaveModel(g.ledger, namespace, &m); err != nil {
		return err
	}
	g.event("create", namespace, fmt.Sprintf("canonical model: %d entities, %d contract fields, %d initial conditions",
		len(m.Entities), len(m.ProjectFields()), len(m.Init)))
	return nil
}

// ensureCollectionInit is the SAFETY NET for designed initial conditions: if a collection
// entity has NO initial condition at all, the designer omitted to scatter it and it would
// boot with every instance stacked at zero (a single dot, not a field). Add a sensible
// scatter — positions uniform across the screen, velocities a small uniform nudge — so a
// particle system always boots as a spread. The designer's OWN init (any entry for that
// entity) is left untouched; this fires only on omission. Returns the entities it backfilled.
func ensureCollectionInit(m *evolution.SystemModel) []string {
	const sw, sh = 320.0, 240.0
	has := map[string]bool{}
	for _, ic := range m.Init {
		has[strings.ToLower(ic.Entity)] = true
	}
	var filled []string
	for _, e := range m.Entities {
		if e.Cardinality <= 1 || has[strings.ToLower(e.Name)] {
			continue
		}
		for _, f := range e.Fields {
			n := strings.ToLower(f.Name)
			ic := evolution.FieldInit{Entity: e.Name, Field: f.Name, Dist: "zero"}
			switch {
			case isVelocityName(n):
				ic.Dist, ic.Min, ic.Max = "uniform", -1, 1
			case isYName(n):
				ic.Dist, ic.Min, ic.Max = "uniform", 0, sh
			case isXName(n):
				ic.Dist, ic.Min, ic.Max = "uniform", 0, sw
			}
			m.Init = append(m.Init, ic)
		}
		filled = append(filled, e.Name)
	}
	return filled
}

// projectContract derives (or re-derives) the shared-state contract from the model by the
// model's ONE projection rule, packs offsets, fills init backstops, and persists it. This
// is what makes the contract a projection of the model rather than an independent
// authoring — names and types are canonical, and no phantom field can appear. Returns
// false (no error) when there is no model to project.
func (g *Grower) projectContract(namespace string) (bool, error) {
	m := evolution.LoadModel(g.ledger, namespace)
	if m == nil {
		return false, nil
	}
	// ProjectFields already assigns absolute offsets AND the interleaved stride for a
	// collection, so do NOT re-pack (packOffsets would re-lay everything out densely and
	// destroy the array-of-structs interleaving). Just fill init backstops.
	fields := m.ProjectFields()
	if len(fields) == 0 {
		return false, nil
	}
	c := &evolution.AppContract{Fields: fields}
	fillInit(c)
	if err := evolution.SaveContract(g.ledger, namespace, c); err != nil {
		return false, err
	}
	return true, nil
}

// resolvePortsToModel rewrites every component's declared reads/writes to the CANONICAL
// projected field names of the model, so a port can only name real modeled state. This
// replaces the old "mint a phantom contract field for any unmatched port" behavior (which
// is exactly what produced particle_states / attractor_pos): a port that names a real
// field is kept, one that names an entity or "<entity>_state(s)" is expanded to that
// entity's fields, a bare field that uniquely matches one entity is prefixed, and anything
// that resolves to nothing is dropped. No-op when there is no model. Best-effort.
func (g *Grower) resolvePortsToModel(namespace string) {
	m := evolution.LoadModel(g.ledger, namespace)
	env := LoadEnvelope(g.ledger, namespace)
	if m == nil || env == nil {
		return
	}
	projected := m.FieldNames()
	changed := false
	for i := range env.SubsystemRequirements {
		s := &env.SubsystemRequirements[i]
		if r := g.resolvePortList(m, projected, s.Reads, namespace); !equalStrs(r, s.Reads) {
			s.Reads, changed = r, true
		}
		if w := g.resolvePortList(m, projected, s.Writes, namespace); !equalStrs(w, s.Writes) {
			s.Writes, changed = w, true
		}
	}
	if changed {
		saveEnvelope(g.ledger, env)
	}
}

func (g *Grower) resolvePortList(m *evolution.SystemModel, projected map[string]bool, ports []string, namespace string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(name string) {
		if name != "" && !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	for _, p := range ports {
		tok := strings.TrimSpace(p)
		switch {
		case tok == "" :
			// skip
		case strings.EqualFold(tok, "HMI input"):
			add("HMI input")
		case projected[tok]:
			add(tok) // already canonical
		default:
			if fields := m.ResolvePort(tok); len(fields) > 0 {
				for _, f := range fields {
					add(f)
				}
			} else {
				evolution.AddPromptGrievance(g.ledger, "model",
					"a component port named state that is not in the model ("+tok+"); every port must name a modeled entity field")
			}
		}
	}
	return out
}

func equalStrs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

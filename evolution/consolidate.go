package evolution

// Consolidation is the steady-state "collapse into common components" pass: at a
// global fixpoint, look ACROSS the settled cells for redundancy — two cells
// doing substantially overlapping work — and propose collapsing them into one.
// A proposal is only ever *attempted* via RunFusion, which is gauntlet-gated, so
// a bad merge is rejected (behavior must be preserved) rather than committed.

import (
	"context"
	"encoding/json"
)

// consolidationCriticPrompt steers the redundancy critic.
const consolidationCriticPrompt = `You are a STRICT software architect auditing a set of cells for REDUNDANCY.
Each cell is a single-responsibility unit, described by what it does. Decide
whether TWO of them do substantially OVERLAPPING work and should COLLAPSE into
one shared component. Only propose a merge when the overlap is real and the two
genuinely duplicate logic; when in doubt, do NOT merge.

Output ONLY JSON, no prose or fences:
{"consolidate": false}
or
{"consolidate": true, "target": "<urn to keep>", "partner": "<urn to fold in>", "reason": "<short>"}`

type consolidationVerdict struct {
	Consolidate bool   `json:"consolidate"`
	Target      string `json:"target"`
	Partner     string `json:"partner"`
	Reason      string `json:"reason"`
}

// ConsolidationCandidate asks the redundancy critic whether two of the given
// cells should collapse into one, returning the pair (target kept, partner
// folded in) or empty strings if none. The pair is validated to be two distinct
// members of urns.
func (o *Orchestrator) ConsolidationCandidate(ctx context.Context, urns []string) (target, partner, reason string, err error) {
	if len(urns) < 2 {
		return "", "", "", nil
	}
	in := map[string]bool{}
	var cells []map[string]string
	for _, u := range urns {
		desc, e := o.repo.Load(u)
		if e != nil {
			continue
		}
		in[u] = true
		cells = append(cells, map[string]string{"identity": u, "does": desc.Semantics.FunctionalIntent})
	}
	if len(cells) < 2 {
		return "", "", "", nil
	}
	payload, _ := json.Marshal(map[string]any{"cells": cells})
	resp, err := o.model.InvokeReasoning(ctx, consolidationCriticPrompt, string(payload))
	if err != nil {
		return "", "", "", err
	}
	js := extractJSONObject(resp)
	if js == "" {
		return "", "", "", nil
	}
	var v consolidationVerdict
	if json.Unmarshal([]byte(js), &v) != nil || !v.Consolidate {
		return "", "", "", nil
	}
	if v.Target == v.Partner || !in[v.Target] || !in[v.Partner] {
		return "", "", "", nil // hallucinated / invalid pair
	}
	return v.Target, v.Partner, v.Reason, nil
}

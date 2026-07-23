package appgen

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/mdcfrancis/flow/evolution"
)

const policyOptimizerPrompt = `You tune the POLICY of an automated build system — the scalar judgement calls that trade off
correctness, speed, and cost. You are given the SYSTEM GOAL, the CURRENT policy (JSON), the
meaning of each field, and FEEDBACK/observations. Propose an adjusted policy that better serves
the GOAL.

Field meanings (all integers):
- maxStallRetries: build attempts before a stuck cell escalates. Higher = more persistence
  (more likely correct) but more tokens; lower = cheaper but may give up too soon.
- grievanceThreshold: malformed-output complaints before a prompt is auto-refined. Lower =
  fixes prompts sooner; higher = waits for a stronger signal.
- visionIntervalSec / codeCriticIntervalSec: seconds between the (costly) adversarial critics.
  Lower = tighter correctness feedback but more cost; higher = cheaper but slower to catch a
  regression.
You MAY also adjust "modelCostWeights" — an optional object mapping a model TYPE
(reason|code|vision|fast) to its relative cost per token (a positive number ~0.1..10; fast is
cheapest, reason dearest). Raising a type's weight makes the loop treat that model as more
expensive and prefer cheaper types where they suffice. Only touch it if the cost signal
warrants; omit it otherwise. Do NOT invent or change "modelBindings" (model ids) — leave them out.

Change ONLY what the feedback/goal justifies; keep the rest. Values are clamped to safe bounds
afterward, so stay reasonable. Output ONLY the adjusted policy as JSON, no prose.`

// OptimizePolicy adjusts the tunable policy toward the SYSTEM GOAL from feedback/observations.
// The LLM proposes new values; clamping to safe bounds (in SavePolicy) is the guardrail — a
// bad tune can shift the balance but never wedge the loop. Returns the applied policy, whether
// it changed, and a note.
func (g *Grower) OptimizePolicy(ctx context.Context, feedback string) (evolution.Policy, bool, string, error) {
	cur := evolution.LoadPolicy(g.ledger)
	curJSON, _ := json.Marshal(cur)
	payload := fmt.Sprintf("SYSTEM GOAL: %s\n\nCURRENT POLICY:\n%s\n\nFEEDBACK / OBSERVATIONS:\n%s",
		evolution.SystemObjective, string(curJSON), strings.TrimSpace(feedback))
	resp, err := g.model.InvokeReasoning(ctx, g.prompt("policy-optimizer", policyOptimizerPrompt), payload)
	if err != nil {
		return cur, false, "", err
	}
	js := extractJSON(resp)
	if js == "" {
		return cur, false, "no policy in model output", nil
	}
	next := cur
	if json.Unmarshal([]byte(js), &next) != nil {
		return cur, false, "unparseable policy", nil
	}
	next.ModelBindings = cur.ModelBindings // bindings are operator-set, never LLM-tuned
	if reflect.DeepEqual(next, cur) {
		return cur, false, "no change proposed", nil
	}
	if err := evolution.SavePolicy(g.ledger, next); err != nil {
		return cur, false, "persist failed: " + err.Error(), nil
	}
	applied := evolution.LoadPolicy(g.ledger) // re-read to show the clamped result
	g.event("policy", "system", fmt.Sprintf("policy tuned: %+v", applied))
	return applied, true, fmt.Sprintf("%+v", applied), nil
}

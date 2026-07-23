package appgen

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"

	"github.com/mdcfrancis/flow/evolution"
)

// promptDefaults maps a refinable prompt's NAME to its hand-written default, so the optimizer
// can be driven by name (from an operator) and always recover the baseline text. Every
// prompt resolved via g.prompt / ResolvePrompt is registered here so it can be refined.
var promptDefaults = map[string]string{
	// Core synthesis (evolution) — the biggest lever.
	"build": evolution.DefaultBuildPrompt,
	// Architecture & authoring (appgen).
	"envelope":            envelopePrompt,
	"contract":            contractPrompt,
	"plan":                planPrompt,
	"component-plan":      componentPlanPrompt,
	"acceptance":          acceptancePrompt,
	"scenario":            scenarioPrompt,
	"leaf-scenario":       leafScenarioPrompt,
	"fracture":            fracturePrompt,
	"architecture-critic": architectureCriticPrompt,
	"motion-judge":        motionJudgePrompt,
	"narrative":           narrativePrompt,
	"wit":                 witPrompt,
	// Feedback loop + optimizers (appgen).
	"feedback":         feedbackPrompt,
	"boundary":         boundaryPrompt,
	"code-critic":      codeCriticPrompt,
	"policy-optimizer": policyOptimizerPrompt,
	"difficulty":       difficultyPrompt,
}

// prompt resolves a named prompt to its stored override, or the hand-written default. Every
// refinable prompt use-site calls this, so an optimized prompt takes effect immediately.
func (g *Grower) prompt(name, def string) string {
	return evolution.ResolvePrompt(g.ledger, name, def)
}

// RefinablePrompts lists the prompt names that can be optimized.
func (g *Grower) RefinablePrompts() []string {
	names := make([]string, 0, len(promptDefaults))
	for n := range promptDefaults {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// AutoOptimizePrompts is the automatic loop: any refinable prompt that has accumulated
// enough GRIEVANCES (objective, recurring complaints about its output — invalid JSON,
// invented fields, hallucinated cells) is refined from those grievances, adversarially
// validated, and adopted only if safe. Grievances are cleared once a prompt is refined.
// Cheap when there are no grievances (just ledger reads). Returns how many were adopted.
func (g *Grower) AutoOptimizePrompts(ctx context.Context) int {
	threshold := evolution.LoadPolicy(g.ledger).GrievanceThreshold
	adopted := 0
	for _, name := range g.RefinablePrompts() {
		gr := evolution.LoadPromptGrievances(g.ledger, name)
		if len(gr) < threshold {
			continue
		}
		feedback := "The prompt's outputs have repeatedly been invalid in these ways:\n- " +
			strings.Join(gr, "\n- ") + "\nRevise the prompt to prevent these failures, keeping the exact output contract."
		_, ok, reason, err := g.OptimizePromptByName(ctx, name, feedback)
		if err != nil {
			log.Printf("[PROMPT] auto-optimize %q failed: %v", name, err)
			continue
		}
		if ok {
			evolution.ClearPromptGrievances(g.ledger, name)
			log.Printf("[PROMPT] auto-refined %q after %d grievance(s): %s", name, len(gr), reason)
			adopted++
		} else {
			log.Printf("[PROMPT] auto-optimize %q not adopted (%s) — keeping default, grievances retained", name, reason)
		}
	}
	return adopted
}

// OptimizePromptByName refines a refinable prompt (resolved by name to its current override
// or default) from feedback, adopting it only if the adversarial validator approves.
func (g *Grower) OptimizePromptByName(ctx context.Context, name, feedback string) (revised string, adopted bool, reason string, err error) {
	def, ok := promptDefaults[name]
	if !ok {
		return "", false, "", fmt.Errorf("unknown prompt %q (refinable: %v)", name, g.RefinablePrompts())
	}
	return g.OptimizePrompt(ctx, name, g.prompt(name, def), feedback)
}

const promptOptimizerPrompt = `You improve a PROMPT used inside an automated system. You are given the prompt's NAME, its
CURRENT text, and FEEDBACK about how its outputs fall short. Rewrite the prompt to address the
feedback while PRESERVING its contract exactly:
- Keep the SAME required output format and field names verbatim (if it demands a specific JSON
  shape, the revision must demand the identical shape).
- Keep every hard constraint and safety rule; you may clarify, reorder, or add guidance, but
  never drop a requirement.
- Change only what the feedback calls for; do not expand scope.
Output ONLY the improved prompt text — no preamble, no fences, no commentary.`

const promptValidatorPrompt = `You are an ADVERSARIAL reviewer guarding an automated pipeline. Given an ORIGINAL prompt and a
REVISED prompt, decide whether the revision is SAFE to adopt: it must demand the SAME output
contract (identical required format/fields) and must not drop any hard constraint or safety
rule the original had. Try to find a reason it is UNSAFE. Output ONLY JSON:
{"safe": <true|false>, "reason": "<short — name the first contract/rule the revision breaks, or why it is safe>"}`

// OptimizePrompt rewrites a named prompt from feedback and adopts it ONLY if an adversarial
// validator confirms the revision preserves the prompt's output contract and constraints —
// so the system can tune its own prompts without silently breaking the pipeline that consumes
// them. Returns the revised text, whether it was adopted, and the validator's reason.
func (g *Grower) OptimizePrompt(ctx context.Context, name, current, feedback string) (revised string, adopted bool, reason string, err error) {
	current = strings.TrimSpace(current)
	if current == "" || strings.TrimSpace(feedback) == "" {
		return "", false, "", fmt.Errorf("prompt and feedback required")
	}
	payload := fmt.Sprintf("SYSTEM GOAL (optimize the prompt so its outputs serve this): %s\n\nPROMPT NAME: %s\n\nCURRENT PROMPT:\n%s\n\nFEEDBACK:\n%s",
		evolution.SystemObjective, name, current, feedback)
	resp, err := g.model.InvokeReasoning(ctx, promptOptimizerPrompt, payload)
	if err != nil {
		return "", false, "", err
	}
	revised = strings.TrimSpace(stripFences(resp))
	if revised == "" || revised == current {
		return revised, false, "no change proposed", nil
	}
	// Adversarially validate the revision before adopting it.
	vresp, verr := g.model.InvokeReasoning(ctx, promptValidatorPrompt,
		fmt.Sprintf("ORIGINAL PROMPT:\n%s\n\nREVISED PROMPT:\n%s", current, revised))
	if verr != nil {
		return revised, false, "validator failed: " + verr.Error(), nil
	}
	safe, why := validatorVerdict(vresp)
	reason = why
	if !safe {
		return revised, false, reason, nil // keep the default — the revision breaks the contract
	}
	if err := evolution.SavePromptOverride(g.ledger, name, revised); err != nil {
		return revised, false, "persist failed: " + err.Error(), nil
	}
	g.event("prompt", name, "prompt refined and adopted: "+reason)
	return revised, true, reason, nil
}

// stripFences removes a leading/trailing ``` code fence if the model wrapped the prompt.
func stripFences(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			s = s[i+1:]
		}
		s = strings.TrimSuffix(strings.TrimSpace(s), "```")
	}
	return strings.TrimSpace(s)
}

func validatorVerdict(resp string) (safe bool, reason string) {
	js := extractJSON(resp)
	if js == "" {
		return false, "no verdict"
	}
	var v struct {
		Safe   bool   `json:"safe"`
		Reason string `json:"reason"`
	}
	if json.Unmarshal([]byte(js), &v) != nil {
		return false, "unparseable verdict"
	}
	return v.Safe, v.Reason
}

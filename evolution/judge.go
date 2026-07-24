package evolution

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/mdcfrancis/flow/inference"
)

// progressJudgePrompt drives the DFS "keep iterating vs unwind" decision: given a
// stalled cell's working draft and the checks it fails, is the draft on a
// promising path (refine it further) or fundamentally blocked (unwind and try a
// different approach)? It defaults to PROGRESSING — we only unwind when clearly
// stuck, so a slow-but-improving cell is never abandoned prematurely.
const progressJudgePrompt = `You judge whether a work-in-progress program is making PROGRESS toward its goal or
is fundamentally BLOCKED, so a synthesis loop can decide whether to keep refining
THIS draft or unwind and start a different approach.

You are given the GOAL, the current DRAFT, and the acceptance CHECKS it currently
fails (each with expected vs actual). Decide:
- PROGRESSING: the draft implements part of the goal correctly and the failing
  checks are addressable by REFINING this draft — a wrong constant, a missing
  case, an off-by-one, an unhandled edge. The approach is sound.
- BLOCKED: the draft is on the wrong track — it misreads the goal, or the failures
  need a structure it is not converging toward, or it has not changed across
  attempts. Refining it further is unlikely to help.

Bias toward PROGRESSING; only say BLOCKED when the draft is clearly stuck. Output
ONLY JSON: {"progressing": true|false, "reason": "<one short line>"}`

// JudgeProgress asks the model whether a stalled cell's working draft is still
// progressing toward its goal. It fails SAFE toward "keep iterating" (returns
// true) whenever it cannot form a confident judgement, so the loop only unwinds on
// an explicit BLOCKED verdict.
func (o *Orchestrator) JudgeProgress(ctx context.Context, urn string) (progressing bool, reason string) {
	draft := LoadFluxDraft(o.ledger, urn)
	if draft == "" {
		return false, "no working draft to iterate on"
	}
	layout := o.fluxLayoutFor(urn)
	if layout == nil {
		return true, "not a flux cell — keep iterating"
	}
	bc, err := lowerFluxToBytecode(layout, draft)
	if err != nil {
		return true, "draft did not lower — keep iterating"
	}
	suite, _ := LoadAcceptance(o.ledger, urn)
	contract := entryContractFor(draft)
	reasons := SuiteFailureReasons(ctx, bc, contract.Name, suite, o.PayloadOffset, o.StateWindow, o.resolver())
	if len(reasons) == 0 {
		return true, "all checks pass — should commit"
	}
	intent := ""
	if desc, e := o.repo.Load(urn); e == nil {
		intent = desc.Semantics.FunctionalIntent
	}
	payload, _ := json.Marshal(map[string]any{
		"goal":           intent,
		"draft":          draft,
		"failing_checks": reasons,
	})
	resp, err := o.modelFor(inference.ModelReason).InvokeReasoning(ctx, progressJudgePrompt, string(payload))
	if err != nil {
		return true, "judge unavailable — keep iterating"
	}
	js := firstJSONObject(resp)
	var v struct {
		Progressing bool   `json:"progressing"`
		Reason      string `json:"reason"`
	}
	if js == "" || json.Unmarshal([]byte(js), &v) != nil {
		return true, "judge output unparseable — keep iterating"
	}
	return v.Progressing, v.Reason
}

// UnwindDraft clears a cell's working draft — the DFS backtrack. The next
// synthesis restarts from the last committed genome instead of refining a draft
// judged blocked.
func (o *Orchestrator) UnwindDraft(urn string) { ClearFluxDraft(o.ledger, urn) }

func firstJSONObject(s string) string {
	i := strings.IndexByte(s, '{')
	j := strings.LastIndexByte(s, '}')
	if i < 0 || j <= i {
		return ""
	}
	return s[i : j+1]
}

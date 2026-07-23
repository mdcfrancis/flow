package appgen

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"github.com/mdcfrancis/flow/evolution"
	"github.com/mdcfrancis/flow/inference"
)

// maxProactiveFractures bounds the up-front DFS so a mis-calibrated "hard" judgment can't
// decompose an app without limit (FractureCell's subsystem cap is the other stop).
const maxProactiveFractures = 16

const difficultyPrompt = `You judge whether a subsystem can be implemented as ONE small WebAssembly
cell (a run-tick function over shared memory), or whether it is too hard and should be DECOMPOSED
into simpler sub-cells FIRST so the system builds something correct (if not yet efficient).

Bias toward DOABLE: a focused cell can do a loop, branches, integer/fixed-point arithmetic, and a
handful of shared-memory reads/writes. Mark NOT doable ONLY when the subsystem genuinely bundles
MULTIPLE distinct algorithmic stages or heavy numeric work that will not fit one cell — e.g.
"initialize state AND integrate positions AND resolve pairwise collisions", multi-body physics, a
full parser/interpreter, a layout+render pipeline. When in doubt, say doable (a reactive fracture
is the fallback if it turns out too hard).

Respond with EXACTLY one JSON object and nothing else:
{"doable": true|false, "reason": "<one line: why; if not doable, name the natural stages>"}`

// JudgeDifficulty asks the model whether a subsystem is implementable as a single cell. It is the
// descent/stop predicate of the proactive DFS: a doable node is scaffolded whole, a not-doable
// node is decomposed first. FAIL-OPEN — any fault returns doable=true, so a model blip never
// over-decomposes (the reactive stall->fracture ladder remains the backstop).
func (g *Grower) JudgeDifficulty(ctx context.Context, semantics, namespace string) (doable bool, reason string) {
	if g.model == nil {
		return true, ""
	}
	contractText := ""
	if c := evolution.LoadContract(g.ledger, namespace); c != nil {
		contractText = c.Render()
	}
	payload, _ := json.Marshal(map[string]any{"subsystem": semantics, "shared_contract": contractText})
	resp, err := g.modelFor(inference.ModelFast).InvokeReasoning(ctx, g.prompt("difficulty", difficultyPrompt), string(payload))
	if err != nil {
		return true, ""
	}
	js := extractJSON(resp)
	if js == "" {
		return true, ""
	}
	var v struct {
		Doable bool   `json:"doable"`
		Reason string `json:"reason"`
	}
	if json.Unmarshal([]byte(js), &v) != nil {
		return true, ""
	}
	return v.Doable, v.Reason
}

// proactiveDecompose is the up-front DFS: for each application subsystem it judges difficulty and,
// if not doable, FRACTURES it into sub-cells BEFORE the build loop spends attempts on a monolith
// it cannot synthesize. Recursion is natural — a fracture's children are judged on the next pass —
// and bounded by maxProactiveFractures + FractureCell's subsystem cap. Composition drivers and UI
// cells are exempt. Returns the child URNs created (so growAll can report them).
func (g *Grower) proactiveDecompose(ctx context.Context, namespace string, enroll func(string)) []string {
	var created []string
	judged := map[string]bool{}
	for i := 0; i < maxProactiveFractures; i++ {
		env := LoadEnvelope(g.ledger, namespace)
		if env == nil {
			break
		}
		target, reason := "", ""
		for _, sub := range env.SubsystemRequirements {
			if sub.Composition != nil || kindOf(sub) == KindRender || judged[sub.Identity] {
				continue
			}
			judged[sub.Identity] = true
			if doable, r := g.JudgeDifficulty(ctx, sub.Semantics, namespace); !doable {
				target, reason = sub.Identity, r
				break
			}
		}
		if target == "" {
			break // every remaining subsystem judged doable
		}
		var kids []string
		n, err := g.FractureCell(ctx, target, func(u string) {
			kids = append(kids, u)
			if enroll != nil {
				enroll(u)
			}
		}, func(string) {})
		if err != nil || n == 0 {
			continue // judged hard but atomic (or fault) — leave it; reactive fracture is the backstop
		}
		created = append(created, kids...)
		g.event("split", target, fmt.Sprintf("proactively decomposed (too hard to build whole: %s) into %d sub-cells", reason, n))
		log.Printf("[GROW] %s judged too hard to build whole (%s) — proactively decomposed into %d sub-cells before any build attempt", target, reason, n)
	}
	return created
}

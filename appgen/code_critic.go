package appgen

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mdcfrancis/flow/evolution"
)

// CodeViolation is one operator criterion an adversarial code reviewer found violated by a
// specific cell — the non-visual counterpart to the vision critic's verdict.
type CodeViolation struct {
	Cell      string `json:"cell"`
	Criterion string `json:"criterion"`
	Reason    string `json:"reason"`
}

const codeCriticPrompt = `You are a STRICT, ADVERSARIAL code reviewer. You are given CRITERIA the operator
requires and the application's cells (each cell's role, its shared-state read/write ports, and
its WAT code). Find every cell that VIOLATES a criterion — actively try to REFUTE that the
solution complies; do not give the benefit of the doubt.

- Judge ONLY criteria assessable from the CODE, its STRUCTURE, or its declared BEHAVIOR
  (e.g. "coordinate through shared state, not private buffers", "clamp the value to a range").
- SKIP purely VISUAL criteria about how something LOOKS on screen — a separate visual critic
  judges those. Do not report a violation you cannot substantiate from the code.
- Name the specific cell and the concrete reason (what the code does vs what the criterion
  requires).

Output ONLY JSON, no prose or fences — an empty array if every criterion is satisfied:
{"violations":[{"cell":"<urn>","criterion":"<the criterion>","reason":"<concrete, code-grounded>"}]}`

// CriticizeCode is the general adversarial arm of the feedback critic: it judges an app's
// cells against non-visual operator criteria using each cell's role, ports, and CODE, and
// returns the cells that violate a criterion. The caller feeds each violation back as a build
// note and re-opens the cell — the human supplies criteria, this adversary judges. Returns
// nil when there are no criteria or no app map yet.
func (g *Grower) CriticizeCode(ctx context.Context, namespace string, criteria []string) ([]CodeViolation, error) {
	if len(criteria) == 0 {
		return nil, nil
	}
	m := evolution.LoadAppMap(g.ledger, namespace)
	if m == nil || len(m.Components) == 0 {
		return nil, nil
	}
	valid := map[string]bool{}
	var sb strings.Builder
	for i := range m.Components {
		c := &m.Components[i]
		desc, err := g.repo.Load(c.Identity)
		if err != nil {
			continue
		}
		wat, _ := g.repo.Genotype(desc)
		valid[c.Identity] = true
		fmt.Fprintf(&sb, "CELL %s\n  role: %s\n  reads: %s  writes: %s\n  code:\n%s\n\n",
			c.Identity, c.Role, strings.Join(c.Reads, ", "), strings.Join(c.Writes, ", "), strings.TrimSpace(wat))
	}
	if len(valid) == 0 {
		return nil, nil
	}
	var pb strings.Builder
	pb.WriteString("CRITERIA the operator requires:\n")
	for _, c := range criteria {
		fmt.Fprintf(&pb, "- %s\n", c)
	}
	pb.WriteString("\nAPPLICATION CELLS:\n")
	pb.WriteString(sb.String())

	resp, err := g.model.InvokeReasoning(ctx, g.prompt("code-critic", codeCriticPrompt), pb.String())
	if err != nil {
		return nil, err
	}
	js := extractJSON(resp)
	if js == "" {
		return nil, nil
	}
	var out struct {
		Violations []CodeViolation `json:"violations"`
	}
	if json.Unmarshal([]byte(js), &out) != nil {
		return nil, nil
	}
	res := []CodeViolation{}
	for _, v := range out.Violations {
		if valid[v.Cell] && strings.TrimSpace(v.Reason) != "" {
			res = append(res, v)
		} else if !valid[v.Cell] {
			// Reported a violation for a cell that isn't in this app — malformed output.
			evolution.AddPromptGrievance(g.ledger, "code-critic",
				"reported a violation for a cell URN that is not one of the listed application cells (only name cells that were provided)")
		}
	}
	return res, nil
}

package flux

// PROPOSER is the north-star source of language changes (docs/self-hosting-flux.md
// §0.1): the model that generates cells is shown how WELL it is currently generating
// them — the scoreboard — and asked to propose the next language variant that would
// let it generate more reliably in fewer tokens. The language optimizes for its own
// generator. The model call is injected (an `ask` func), so prompt-building and
// parsing are testable without a live model, and the flux package stays free of an
// inference dependency.

import (
	"fmt"
	"strings"
)

// Proposal is the model's chosen next experiment: a point in the knob space plus its
// reasoning.
type Proposal struct {
	Knobs     LanguageKnobs
	Rationale string
}

// ProposeNextVariant asks the model to read the current scoreboard and propose the
// next language variant to try. ask performs the model call (prompt in, completion
// out). The returned Proposal's Knobs are parsed tolerantly from the completion; the
// Rationale is the model's prose minus the directive line.
func ProposeNextVariant(board []Score, ask func(prompt string) (string, error)) (Proposal, error) {
	resp, err := ask(ProposerPrompt(board))
	if err != nil {
		return Proposal{}, err
	}
	return Proposal{Knobs: ParseKnobs(resp), Rationale: extractRationale(resp)}, nil
}

// ProposerPrompt renders the proposer's instruction: the fitness definition, the
// current scoreboard, the knob menu, and the ask.
func ProposerPrompt(board []Score) string {
	var b strings.Builder
	b.WriteString("You are tuning the LANGUAGE you generate cells in, to make your own generation more reliable.\n")
	b.WriteString("FITNESS (higher is better) = valid-rate (floor; must stay ~1.0) + 0.5·canonicality − 0.002·mean-tokens.\n")
	b.WriteString("  valid-rate   = fraction of generations that parse + type-check.\n")
	b.WriteString("  canonicality = fraction of valid generations that are the SAME program (you converging vs scattering).\n")
	b.WriteString("  mean-tokens  = completion tokens per cell (lower is cheaper).\n\n")
	b.WriteString("CURRENT SCOREBOARD (each variant measured over the same benchmark):\n")
	b.WriteString(RenderBoard(board))
	b.WriteString("\n")
	b.WriteString(KnobMenu)
	b.WriteString("\n\nPropose the next variant to try. Reply with EXACTLY one directive line of the form\n")
	b.WriteString("  clauses=<true|false> canonical_writes=<true|false>\n")
	b.WriteString("then one line beginning \"Rationale:\" explaining which fitness axis you are targeting and why.\n")
	return b.String()
}

// RenderBoard formats a scoreboard for the proposer prompt (and for logging).
func RenderBoard(board []Score) string {
	var b strings.Builder
	for rank, s := range board {
		fmt.Fprintf(&b, "  #%d %-24s fitness=%.3f | valid=%.2f tokens=%.1f canonicality=%.2f\n",
			rank+1, s.Variant, s.Fitness(), s.ValidRate, s.MeanTokens, s.Canonicality)
	}
	return b.String()
}

// extractRationale pulls the model's reasoning: the text after a "Rationale:" marker
// if present, else the response minus any directive line.
func extractRationale(resp string) string {
	if i := strings.Index(strings.ToLower(resp), "rationale:"); i >= 0 {
		return strings.TrimSpace(resp[i+len("rationale:"):])
	}
	var kept []string
	for _, line := range strings.Split(resp, "\n") {
		if strings.Contains(strings.ToLower(line), "clauses=") {
			continue // drop the directive line
		}
		if t := strings.TrimSpace(line); t != "" {
			kept = append(kept, t)
		}
	}
	return strings.Join(kept, " ")
}

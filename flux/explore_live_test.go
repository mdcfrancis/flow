package flux

import (
	"os"
	"testing"
)

// TestLanguageEvolutionExplore is the full exploration loop, live: measure the known
// variants, hand the scoreboard to the MODEL, let it propose the next variant to try
// (the north-star LLM-in-the-loop of docs/self-hosting-flux.md §0.1), then measure
// what it proposed. Skipped unless HDM_LIVE_EXPLORE=1. Run:
//
//	HDM_LIVE_EXPLORE=1 HDM_LLM_API_KEY=$(cat .hdm_api_key) go test ./flux/ -run Explore -v -timeout 1200s
func TestLanguageEvolutionExplore(t *testing.T) {
	if os.Getenv("HDM_LIVE_EXPLORE") == "" {
		t.Skip("set HDM_LIVE_EXPLORE=1 to run the live language-evolution exploration")
	}
	url := os.Getenv("HDM_LLM_URL")
	if url == "" {
		url = "http://localhost:8000"
	}
	model := os.Getenv("HDM_LLM_MODEL")
	if model == "" {
		model = "Qwen3.6-27B-oQ4"
	}
	gen := &httpGenerator{url: url, key: os.Getenv("HDM_LLM_API_KEY"), model: model, temperature: 0.7}
	samples := 4

	// 1. Measure the known variants.
	board := RunScoreboard(gen, DefaultBenchmark(), FluxV1Variants(), samples)
	t.Logf("=== scoreboard ===\n%s", RenderBoard(board))

	// 2. The model reads the board and proposes the next variant.
	prop, err := ProposeNextVariant(board, gen.Ask)
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	t.Logf("=== model proposal ===\nknobs: %s\nrationale: %s", prop.Knobs.Directive(), prop.Rationale)

	// 3. Measure exactly what the model proposed.
	proposed := RunScoreboard(gen, DefaultBenchmark(), []Variant{prop.Knobs.Variant()}, samples)
	if len(proposed) == 1 {
		p := proposed[0]
		t.Logf("=== proposed variant measured ===\n%-24s fitness=%.3f | syntax=%.2f valid=%.2f tokens=%.1f canon=%.2f",
			p.Variant, p.Fitness(), p.SyntaxRate, p.ValidRate, p.MeanTokens, p.Canonicality)
		best := board[0].Fitness()
		if p.Fitness() > best {
			t.Logf(">>> the model's proposal BEAT the prior best (%.3f > %.3f) — a candidate for the epoch gate", p.Fitness(), best)
		} else {
			t.Logf(">>> the model's proposal did not beat the prior best (%.3f <= %.3f) — rejected at the cheap tier", p.Fitness(), best)
		}
	}
}

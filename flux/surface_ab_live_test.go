package flux

import (
	"os"
	"strconv"
	"testing"
)

// TestSurfaceAB is the live S-expr vs Forth A/B — the question "which surface does
// the LLM generate more efficiently?" answered by measurement, not guess
// (docs/flux-surface-ir.md). Both surfaces lower through the identical invariant, so
// anything either emits is behavior-safe by BehaviorHash; the scoreboard measures
// syntax rate, TYPE-valid rate, token cost, and canonicality. Skipped unless
// HDM_LIVE_AB=1. Run:
//
//	HDM_LIVE_AB=1 HDM_LLM_API_KEY=$(cat .hdm_api_key) go test ./flux/ -run SurfaceAB -v -timeout 1200s
func TestSurfaceAB(t *testing.T) {
	if os.Getenv("HDM_LIVE_AB") == "" {
		t.Skip("set HDM_LIVE_AB=1 to run the live S-expr vs Forth A/B")
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
	samples := 5
	if n, err := strconv.Atoi(os.Getenv("HDM_AB_SAMPLES")); err == nil && n > 0 {
		samples = n
	}

	// The decisive pair: the S-expression baseline vs the type-stratified Forth
	// surface. HDM_AB_SAMPLES raises the sample count to de-noise the estimate.
	variants := []Variant{
		SExprVariant(),
		ForthTypedVariant("forth-typed", 2),
	}
	board := RunScoreboard(gen, DefaultBenchmark(), variants, samples)
	t.Logf("=== surface A/B (%d samples/task, temp 0.7) ===", samples)
	for rank, s := range board {
		t.Logf("#%d %-9s fitness=%.3f | syntax=%.2f valid=%.2f tokens=%.1f canon=%.2f",
			rank+1, s.Variant, s.Fitness(), s.SyntaxRate, s.ValidRate, s.MeanTokens, s.Canonicality)
	}
	if len(board) != len(variants) {
		t.Fatalf("expected %d surfaces, got %d", len(variants), len(board))
	}
	for _, s := range board {
		if s.Samples == 0 {
			t.Errorf("%s produced no samples", s.Variant)
		}
		if s.SyntaxRate < 0.8 {
			t.Errorf("%s: grammar-constrained syntax rate should be high, got %.2f", s.Variant, s.SyntaxRate)
		}
	}
}

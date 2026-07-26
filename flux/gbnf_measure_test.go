package flux

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"testing"
	"time"
)

// TestGBNFEvolutionMeasure is the north-star scoreboard run live: it A/Bs the
// language variants (baseline grammar with reads/writes clauses vs the evolved terse
// clause-less grammar) on the canonical benchmark and ranks them by generation
// fitness — the cheap micro-tier of language-evolution exploration (see
// flux/scoreboard.go, docs/language-evolution.md). Skipped unless HDM_LIVE_GBNF=1.
// Run:
//
//	HDM_LIVE_GBNF=1 HDM_LLM_API_KEY=$(cat .hdm_api_key) go test ./flux/ -run EvolutionMeasure -v
//
// Sampling at temperature>0 with N samples is what makes CANONICALITY observable: a
// deterministic decode would trivially score 1. The baseline (clauses) must be
// valid-by-construction; the terse variant was measured a NEGATIVE (fewer clause
// tokens but the clauses anchor concision + field vocabulary, so the body bloats and
// invents names). See docs/grammar-constrained-flux.md.
func TestGBNFEvolutionMeasure(t *testing.T) {
	if os.Getenv("HDM_LIVE_GBNF") == "" {
		t.Skip("set HDM_LIVE_GBNF=1 to run the live grammar A/B measurement")
	}
	url := os.Getenv("HDM_LLM_URL")
	if url == "" {
		url = "http://localhost:8000"
	}
	model := os.Getenv("HDM_LLM_MODEL")
	if model == "" {
		model = "Qwen3.6-27B-oQ4"
	}
	samples := 5

	gen := &httpGenerator{url: url, key: os.Getenv("HDM_LLM_API_KEY"), model: model, temperature: 0.7}
	board := RunScoreboard(gen, DefaultBenchmark(), FluxV1Variants(), samples)

	t.Logf("=== language A/B scoreboard (%d samples/task, temp 0.7) ===", samples)
	for rank, s := range board {
		t.Logf("#%d %-24s fitness=%.3f | syntax=%.2f valid=%.2f tokens=%.1f canon=%.2f",
			rank+1, s.Variant, s.Fitness(), s.SyntaxRate, s.ValidRate, s.MeanTokens, s.Canonicality)
	}
	// This is a MEASUREMENT, not a hard gate — the interesting signal (validity gap,
	// canonicality) is inherently noisy at temp>0. Sanity only: every variant was
	// exercised, and the grammar delivers its guarantee (SYNTAX validity ~1.0; the
	// grammar does not guarantee TYPES, so ValidRate legitimately sits below it).
	if len(board) != len(FluxV1Variants()) {
		t.Fatalf("expected a score per variant, got %d", len(board))
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

// httpGenerator is the live Generator: it calls the OpenAI-compatible oMLX endpoint
// with a guided_grammar constraint (the enforced path; see
// docs/grammar-constrained-flux.md) and returns the completion + its token count.
type httpGenerator struct {
	url, key, model string
	temperature     float64
}

func (g *httpGenerator) Generate(prompt, grammar string) (string, int, error) {
	return g.post(prompt, grammar, g.temperature)
}

// Ask is an UNCONSTRAINED chat call (no grammar) — used by the language proposer,
// which authors a directive, not a cell. Low temperature for a decisive proposal.
func (g *httpGenerator) Ask(prompt string) (string, error) {
	s, _, err := g.post(prompt, "", 0.2)
	return s, err
}

func (g *httpGenerator) post(prompt, grammar string, temperature float64) (string, int, error) {
	payload := map[string]any{
		"model":                g.model,
		"messages":             []any{map[string]string{"role": "user", "content": prompt}},
		"max_tokens":           400,
		"temperature":          temperature,
		"chat_template_kwargs": map[string]any{"enable_thinking": false},
	}
	if grammar != "" {
		payload["guided_grammar"] = grammar
	}
	body, _ := json.Marshal(payload)
	req, _ := http.NewRequest(http.MethodPost, g.url+"/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if g.key != "" {
		req.Header.Set("Authorization", "Bearer "+g.key)
	}
	resp, err := (&http.Client{Timeout: 120 * time.Second}).Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || len(out.Choices) == 0 {
		return "", 0, err
	}
	return out.Choices[0].Message.Content, out.Usage.CompletionTokens, nil
}

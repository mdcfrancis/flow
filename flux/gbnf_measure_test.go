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

// TestGBNFEvolutionMeasure is the north-star scoreboard: it A/Bs the baseline
// grammar (with reads/writes clauses) against the evolved terse grammar
// (clause-less, derived) on the two fitness axes we can measure here —
// syntax-valid rate (must stay ~100%: both are grammar-constrained) and TOKEN COST.
// The evolution wins if, at equal validity, it costs fewer tokens. Skipped unless
// HDM_LIVE_GBNF=1. Run:
//
//	HDM_LIVE_GBNF=1 HDM_LLM_API_KEY=$(cat .hdm_api_key) go test ./flux/ -run EvolutionMeasure -v
func TestGBNFEvolutionMeasure(t *testing.T) {
	if os.Getenv("HDM_LIVE_GBNF") == "" {
		t.Skip("set HDM_LIVE_GBNF=1 to run the live grammar A/B measurement")
	}
	url := os.Getenv("HDM_LLM_URL")
	if url == "" {
		url = "http://localhost:8000"
	}
	key := os.Getenv("HDM_LLM_API_KEY")
	model := os.Getenv("HDM_LLM_MODEL")
	if model == "" {
		model = "Qwen3.6-27B-oQ4"
	}
	layout := Layout{
		"ball_x": {Type: TInt, Offset: 0xB0000}, "ball_y": {Type: TInt, Offset: 0xB0004},
		"ball_vx": {Type: TInt, Offset: 0xB0008}, "ball_vy": {Type: TInt, Offset: 0xB000C},
		"screen_w": {Type: TInt, Offset: 0xB0010}, "screen_h": {Type: TInt, Offset: 0xB0014},
	}
	objectives := []struct {
		name, prompt string
		kind         CellKind
	}{
		{"physics", "Move the ball: add each velocity to each position, and reflect the velocity at the walls.", KindCompute},
		{"input", "Set ball_vx to ball_vx and ball_x to ball_x plus ball_vx.", KindCompute},
		{"view", "Draw the ball as a circle at its position.", KindView},
	}
	// The measured WIN is validity-by-construction: the strict grammar (with clauses)
	// must produce valid Flux for every objective. The terse (clause-less) variant is
	// reported for comparison — it was found to be a NEGATIVE (fewer clause tokens but
	// the clauses anchor concision + field vocabulary, so the body bloats and invents
	// names). See docs/grammar-constrained-flux.md.
	for _, o := range objectives {
		base := gen(t, url, key, model, o.prompt, GBNF(layout, o.kind))
		terse := gen(t, url, key, model, o.prompt, GBNFTerse(layout, o.kind))
		baseOK := valid(base.src, layout)
		terseOK := valid(terse.src, layout)
		t.Logf("%-8s  strict(clauses): %3d tok valid=%v | terse(no clauses): %3d tok valid=%v",
			o.name, base.tokens, baseOK, terse.tokens, terseOK)
		if !baseOK {
			t.Errorf("%s: the strict grammar must produce VALID Flux by construction:\n%s", o.name, base.src)
		}
	}
}

type genResult struct {
	src    string
	tokens int
}

func gen(t *testing.T, url, key, model, prompt, grammar string) genResult {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"model":                model,
		"messages":             []any{map[string]string{"role": "user", "content": prompt}},
		"max_tokens":           400,
		"temperature":          0.0,
		"guided_grammar":       grammar,
		"chat_template_kwargs": map[string]any{"enable_thinking": false},
	})
	req, _ := http.NewRequest(http.MethodPost, url+"/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := (&http.Client{Timeout: 120 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
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
		t.Fatalf("decode: %v (%s)", err, string(raw)[:min(200, len(raw))])
	}
	return genResult{src: out.Choices[0].Message.Content, tokens: out.Usage.CompletionTokens}
}

func valid(src string, layout Layout) bool {
	f, err := Parse("m", src)
	if err != nil {
		return false
	}
	_, err = Check(f, layout)
	return err == nil
}

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

// TestGBNFRoundTripLive proves the north-star mechanism end to end on a real
// server: generate under a per-cell GBNF via `guided_grammar`, and assert the
// output PARSES and CHECKS as valid Flux — validity by construction. Skipped
// unless HDM_LIVE_GBNF=1 (needs the oMLX server + HDM_LLM_API_KEY); never runs in
// CI. Run: HDM_LIVE_GBNF=1 HDM_LLM_API_KEY=$(cat .hdm_api_key) go test ./flux/ -run RoundTripLive -v
func TestGBNFRoundTripLive(t *testing.T) {
	if os.Getenv("HDM_LIVE_GBNF") == "" {
		t.Skip("set HDM_LIVE_GBNF=1 to run the live grammar round-trip")
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
	cases := []struct {
		name, prompt string
		kind         CellKind
	}{
		{"compute", "Move the ball: add each velocity to each position, and reflect the velocity at the walls.", KindCompute},
		{"view", "Draw the ball as a circle at its position.", KindView},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			grammar := GBNF(layout, tc.kind)
			if grammar == "" {
				t.Fatal("empty grammar")
			}
			body, _ := json.Marshal(map[string]any{
				"model":                model,
				"messages":             []any{map[string]string{"role": "user", "content": tc.prompt}},
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
			}
			if err := json.Unmarshal(raw, &out); err != nil || len(out.Choices) == 0 {
				t.Fatalf("decode: %v (%s)", err, string(raw)[:min(200, len(raw))])
			}
			src := out.Choices[0].Message.Content
			t.Logf("constrained %s:\n%s", tc.name, src)
			// The whole point: it must be valid Flux with no repair.
			f, perr := Parse("live", src)
			if perr != nil {
				t.Fatalf("constrained output did not PARSE: %v\nsrc: %s", perr, src)
			}
			if _, cerr := Check(f, layout); cerr != nil {
				t.Fatalf("constrained output parsed but did not CHECK: %v\nsrc: %s", cerr, src)
			}
		})
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

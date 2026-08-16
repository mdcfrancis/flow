package inference

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
)

// Google Gemini API (generateContent) wire types. Only the fields HDM needs.
// https://ai.google.dev/api/generate-content

// geminiInlineData carries a base64-encoded image for multimodal (vision) input.
type geminiInlineData struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

type geminiPart struct {
	Text       string            `json:"text,omitempty"`
	InlineData *geminiInlineData `json:"inlineData,omitempty"`
}

type geminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []geminiPart `json:"parts"`
}

type geminiThinkingConfig struct {
	ThinkingBudget int `json:"thinkingBudget"`
}

type geminiGenConfig struct {
	Temperature     float32               `json:"temperature"`
	MaxOutputTokens int                   `json:"maxOutputTokens"`
	ThinkingConfig  *geminiThinkingConfig `json:"thinkingConfig,omitempty"`
}

// geminiNoThinking DISABLES the model's chain-of-thought. A Gemini "flash" THINKING
// model otherwise spends its entire output-token budget on internal reasoning and
// returns a TRUNCATED or empty answer (finishReason MAX_TOKENS) — so cell synthesis
// came back as "empty source stream" on every cell. This is the Gemini analogue of the
// local server's enable_thinking:false; thinkingBudget 0 is ignored by non-thinking
// models, so it is safe to send unconditionally.
var geminiNoThinking = &geminiThinkingConfig{ThinkingBudget: 0}

type geminiRequestBody struct {
	SystemInstruction *geminiContent  `json:"systemInstruction,omitempty"`
	Contents          []geminiContent `json:"contents"`
	GenerationConfig  geminiGenConfig `json:"generationConfig"`
}

type geminiResponseBody struct {
	Candidates []struct {
		Content      geminiContent `json:"content"`
		FinishReason string        `json:"finishReason"`
	} `json:"candidates"`
	UsageMetadata struct {
		TotalTokenCount int `json:"totalTokenCount"`
	} `json:"usageMetadata"`
	// PromptFeedback surfaces a safety block (no candidates returned).
	PromptFeedback struct {
		BlockReason string `json:"blockReason"`
	} `json:"promptFeedback"`
}

// geminiMaxOutputTokens is generous: a synthesized WAT module can be long, and
// flash-lite output is cheap; truncation would waste the whole call.
const geminiMaxOutputTokens = 8192

// geminiRequest builds the Gemini generateContent POST. The system prompt maps
// to systemInstruction and the user context to a single user turn; the API key
// goes in the x-goog-api-key header. Temperature 0 for determinism.
func (c *LocalModelClient) geminiRequest(ctx context.Context, sysPrompt, userCtx string) (*http.Request, error) {
	payload, err := json.Marshal(geminiRequestBody{
		SystemInstruction: &geminiContent{Parts: []geminiPart{{Text: sysPrompt}}},
		Contents:          []geminiContent{{Role: "user", Parts: []geminiPart{{Text: userCtx}}}},
		GenerationConfig:  geminiGenConfig{Temperature: 0.0, MaxOutputTokens: geminiMaxOutputTokens, ThinkingConfig: geminiNoThinking},
	})
	if err != nil {
		return nil, err
	}
	url := fmt.Sprintf("%s/v1beta/models/%s:generateContent", c.baseURL, c.model)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("x-goog-api-key", c.apiKey)
	}
	return req, nil
}

// geminiVisionRequest builds a MULTIMODAL generateContent POST: the question text
// plus one or more PNG images (inline base64) as a single user turn. This is how
// HDM "sees" — the model evaluates the rendered frames against the question.
func (c *LocalModelClient) geminiVisionRequest(ctx context.Context, sysPrompt, question string, images [][]byte) (*http.Request, error) {
	parts := make([]geminiPart, 0, 1+len(images))
	parts = append(parts, geminiPart{Text: question})
	for _, img := range images {
		parts = append(parts, geminiPart{InlineData: &geminiInlineData{
			MimeType: "image/png", Data: base64.StdEncoding.EncodeToString(img),
		}})
	}
	payload, err := json.Marshal(geminiRequestBody{
		SystemInstruction: &geminiContent{Parts: []geminiPart{{Text: sysPrompt}}},
		Contents:          []geminiContent{{Role: "user", Parts: parts}},
		GenerationConfig:  geminiGenConfig{Temperature: 0.0, MaxOutputTokens: geminiMaxOutputTokens, ThinkingConfig: geminiNoThinking},
	})
	if err != nil {
		return nil, err
	}
	url := fmt.Sprintf("%s/v1beta/models/%s:generateContent", c.baseURL, c.model)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("x-goog-api-key", c.apiKey)
	}
	return req, nil
}

// parseGemini extracts the generated text (concatenating parts) and the total
// token count from a Gemini response body.
func parseGemini(body []byte) (string, int, error) {
	var r geminiResponseBody
	if err := json.Unmarshal(body, &r); err != nil {
		return "", 0, fmt.Errorf("malformed Gemini response: %w", err)
	}
	if len(r.Candidates) == 0 {
		if r.PromptFeedback.BlockReason != "" {
			return "", 0, fmt.Errorf("Gemini blocked the prompt: %s", r.PromptFeedback.BlockReason)
		}
		return "", 0, fmt.Errorf("Gemini returned no candidates")
	}
	var b bytes.Buffer
	for _, p := range r.Candidates[0].Content.Parts {
		b.WriteString(p.Text)
	}
	return b.String(), r.UsageMetadata.TotalTokenCount, nil
}

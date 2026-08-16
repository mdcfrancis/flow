package inference

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"
)

// ErrServerUnreachable marks a reasoning failure caused by the model server
// being offline/unreachable (connection refused, reset, timeout) rather than by
// the model's output. Callers use errors.Is to tell an INFRASTRUCTURE outage
// apart from a genuine synthesis failure — e.g. so a transient outage does not
// count as a convergence "hold" that parks a cell.
var ErrServerUnreachable = errors.New("model server unreachable")

// apiKeyFile is the gitignored local file holding the bearer token, used when
// no HDM_LLM_API_KEY / OPENAI_API_KEY environment variable is set.
const apiKeyFile = ".hdm_api_key"

// InferenceRequest is the JSON payload sent to the OpenAI-compatible endpoint.
type InferenceRequest struct {
	Model       string        `json:"model"`
	Messages    []ChatMessage `json:"messages"`
	Temperature float32       `json:"temperature"`
	MaxTokens   int           `json:"max_tokens"`
	// ChatTemplateKwargs passes template flags to the local server; we use it to
	// suppress a reasoning model's chain-of-thought (see disableThinking).
	ChatTemplateKwargs map[string]any `json:"chat_template_kwargs,omitempty"`
}

// disableThinking suppresses a reasoning model's chain-of-thought on the local
// OpenAI-compatible server. Qwen3 "thinking" variants otherwise dump a long
// reasoning trace that exhausts the token budget (never reaching the answer) and
// exceeds the request timeout — the "malformed completion / unexpected end of
// JSON" failure. It is the nested chat_template flag (a top-level enable_thinking
// is ignored), and it is harmless to non-thinking models, which ignore the
// unknown template key.
var disableThinking = map[string]any{"enable_thinking": false}

// ChatMessage represents a single message in the conversation array.
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// InferenceResponse is the JSON structure returned by the OpenAI-compatible endpoint.
type InferenceResponse struct {
	Choices []Choice `json:"choices"`
	Usage   Usage    `json:"usage"`
}

// Usage reports token consumption for a completion.
type Usage struct {
	TotalTokens int `json:"total_tokens"`
}

// Choice represents a single completion choice.
type Choice struct {
	Message ChatMessage `json:"message"`
}

// Provider selects the wire format / endpoint a client speaks.
const (
	providerOpenAI = "openai" // OpenAI-compatible /v1/chat/completions (local MLX server)
	providerGemini = "gemini" // Google Gemini generateContent API
)

// LocalModelClient connects to a cognitive-engine backend. Despite the name it
// speaks either the local OpenAI-compatible server (providerOpenAI, the default)
// or the Google Gemini API (providerGemini); the provider selects the endpoint,
// auth, request shape, and response parsing while everything else — reconnect,
// token accounting, the Observe hook — is shared.
type LocalModelClient struct {
	provider    string
	baseURL     string
	model       string
	apiKey      string
	client      *http.Client
	reconnect   time.Duration // total window to keep retrying a transient outage
	totalTokens atomic.Uint64 // cumulative tokens consumed across all calls
	// Observe, when set, is called after every successful reasoning round-trip
	// with the data that flowed: the derived purpose (from the system prompt),
	// prompt/response byte sizes, tokens consumed, and wall duration. Used to
	// feed the live data-flow log. Must be safe for concurrent use.
	Observe func(purpose string, promptBytes, respBytes, tokens int, ms int64)
}

// TotalTokens returns the cumulative token count consumed by this client — the
// raw cognitive compute cost feeding the Hamiltonian's ComputeCost term.
func (c *LocalModelClient) TotalTokens() uint64 { return c.totalTokens.Load() }

// NewLocalModelClient creates a client pointing at the local LLM server. The
// local MLX server validates the bearer token, resolved in order from the
// HDM_LLM_API_KEY env var, the OPENAI_API_KEY env var, the gitignored
// .hdm_api_key file, and finally the placeholder "<na>" (which keeps the header
// present but will not authenticate against an auth-enabled server).
func NewLocalModelClient(baseURL, model string) *LocalModelClient {
	apiKey := os.Getenv("HDM_LLM_API_KEY")
	if apiKey == "" {
		apiKey = os.Getenv("OPENAI_API_KEY")
	}
	if apiKey == "" {
		if b, err := os.ReadFile(apiKeyFile); err == nil {
			apiKey = strings.TrimSpace(string(b))
		}
	}
	if apiKey == "" {
		apiKey = "<na>"
	}
	// Reconnect window: how long a single InvokeReasoning keeps retrying a transient
	// model-server outage (connection refused / 5xx / timeout) before giving up.
	// Kept modest so no one call freezes the caller for minutes — a server restart
	// is covered by this window, and a longer outage is covered by callers retrying
	// across cycles (the bootstrap each tick, growth every 30s, evolution each
	// frame). Override with HDM_LLM_RECONNECT (a Go duration); 0 disables retries.
	reconnect := 45 * time.Second
	if v := os.Getenv("HDM_LLM_RECONNECT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d >= 0 {
			reconnect = d
		}
	}
	// Per-request HTTP timeout. Non-streaming completions return headers only when
	// the whole generation is done, so the WHOLE generation must fit inside this
	// window — a slow reasoning model on a large prompt otherwise has its response
	// cut mid-body. Measured: the default Qwen3.8-27B-oQ4 takes >5min on a full
	// macro-WAT synthesis prompt (a 27B model on local hardware), where the old
	// 120s default truncated every one of them. Lower it with HDM_LLM_TIMEOUT when
	// pointing at a fast hosted backend.
	timeout := 600 * time.Second
	if v := os.Getenv("HDM_LLM_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			timeout = d
		}
	}
	return &LocalModelClient{
		provider:  providerOpenAI,
		baseURL:   baseURL,
		model:     model,
		apiKey:    apiKey,
		reconnect: reconnect,
		client: &http.Client{
			Timeout: timeout,
		},
	}
}

// GeminiBaseURL is the default Google Generative Language API endpoint.
const GeminiBaseURL = "https://generativelanguage.googleapis.com"

// NewGeminiClient creates a client that speaks the Google Gemini API. baseURL
// defaults to GeminiBaseURL when empty. The apiKey is sent in the x-goog-api-key
// header. Shares the reconnect window (HDM_LLM_RECONNECT), token accounting, and
// Observe hook with the OpenAI client.
func NewGeminiClient(baseURL, model, apiKey string) *LocalModelClient {
	c := NewLocalModelClient(baseURL, model)
	if baseURL == "" {
		c.baseURL = GeminiBaseURL
	}
	c.provider = providerGemini
	c.apiKey = apiKey
	return c
}

// InvokeReasoning sends system_prompt and user_context to the local model and
// returns the generated text. Temperature is clamped to 0.0 for deterministic,
// reproducible completions.
//
// Reconnect: a transient model-server outage (connection refused / timeout /
// 5xx — e.g. the server restarting or swapping models) is retried with
// exponential backoff for up to the reconnect window, so callers (growth,
// evolution, the bootstrap tick) survive a blip instead of failing hard.
// Permanent errors (4xx auth/bad-request, malformed body) fail fast, and ctx
// cancellation stops the wait immediately.
func (c *LocalModelClient) InvokeReasoning(ctx context.Context, sysPrompt string, userCtx string) (string, error) {
	return c.retryLoop(ctx, func(ctx context.Context) (string, bool, error) {
		return c.attempt(ctx, sysPrompt, userCtx)
	})
}

// ErrVisionUnsupported is returned by InvokeVision when there is nothing to see.
var ErrVisionUnsupported = errors.New("vision (multimodal) call had no images")

// InvokeVision asks the model a natural-language question about one or more
// rendered frames (PNG images) — the vision path. Works on either backend: Gemini
// via generateContent, or the local OpenAI-compatible server via image_url content
// (verified against omlx Gemma-4). Shares the reconnect/retry window.
func (c *LocalModelClient) InvokeVision(ctx context.Context, sysPrompt, question string, images [][]byte) (string, error) {
	if len(images) == 0 {
		return "", ErrVisionUnsupported
	}
	return c.retryLoop(ctx, func(ctx context.Context) (string, bool, error) {
		return c.visionAttempt(ctx, sysPrompt, question, images)
	})
}

// retryLoop runs send with exponential backoff over the reconnect window, so a
// transient model-server outage is ridden out rather than surfaced as a failure.
func (c *LocalModelClient) retryLoop(ctx context.Context, send func(context.Context) (string, bool, error)) (string, error) {
	deadline := time.Now().Add(c.reconnect)
	backoff := time.Second
	for attempt := 0; ; attempt++ {
		out, retriable, err := send(ctx)
		if err == nil {
			return out, nil
		}
		// Permanent error, retries disabled, budget spent, or ctx done: give up.
		if !retriable || c.reconnect <= 0 || time.Now().After(deadline) || ctx.Err() != nil {
			if attempt > 0 {
				return "", fmt.Errorf("model server unreachable after %d retries over ~%s: %w", attempt, c.reconnect, err)
			}
			return "", err
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(backoff):
		}
		if backoff < 8*time.Second {
			backoff *= 2
		}
	}
}

// visionAttempt sends one multimodal request and parses the verdict, tagging a
// transient network/5xx/429 error as retriable (like attempt()).
func (c *LocalModelClient) visionAttempt(ctx context.Context, sysPrompt, question string, images [][]byte) (content string, retriable bool, err error) {
	var httpReq *http.Request
	if c.provider == providerGemini {
		httpReq, err = c.geminiVisionRequest(ctx, sysPrompt, question, images)
	} else {
		httpReq, err = c.openaiVisionRequest(ctx, sysPrompt, question, images)
	}
	if err != nil {
		return "", false, fmt.Errorf("failed to construct vision request: %w", err)
	}
	start := time.Now()
	resp, err := c.client.Do(httpReq)
	if err != nil {
		return "", true, fmt.Errorf("%w: %v", ErrServerUnreachable, err)
	}
	defer resp.Body.Close()
	body, rerr := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		retry := resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests
		return "", retry, fmt.Errorf("vision server returned status %d: %s", resp.StatusCode, string(body))
	}
	if rerr != nil {
		return "", true, fmt.Errorf("%w: truncated vision response after %d bytes: %v", ErrServerUnreachable, len(body), rerr)
	}
	var tokens int
	if c.provider == providerGemini {
		content, tokens, err = parseGemini(body)
	} else {
		content, tokens, err = parseOpenAI(body)
	}
	if err != nil {
		return "", false, err
	}
	c.totalTokens.Add(uint64(tokens))
	if c.Observe != nil {
		c.Observe("vision", len(sysPrompt)+len(question), len(content), tokens, time.Since(start).Milliseconds())
	}
	return content, false, nil
}

// attempt performs one reasoning round-trip. It returns retriable=true when the
// failure is transient (connection error / timeout / 5xx) so the caller may back
// off and try again; retriable=false for permanent failures (4xx, malformed).
// The provider selects the request shape and response parsing.
func (c *LocalModelClient) attempt(ctx context.Context, sysPrompt, userCtx string) (content string, retriable bool, err error) {
	var httpReq *http.Request
	if c.provider == providerGemini {
		httpReq, err = c.geminiRequest(ctx, sysPrompt, userCtx)
	} else {
		httpReq, err = c.openaiRequest(ctx, sysPrompt, userCtx)
	}
	if err != nil {
		return "", false, fmt.Errorf("failed to construct request: %w", err)
	}

	start := time.Now()
	resp, err := c.client.Do(httpReq)
	if err != nil {
		// Connection refused / reset / timeout: server down or restarting — retry,
		// and tag it ErrServerUnreachable so callers can distinguish an outage from
		// a genuine synthesis failure.
		return "", true, fmt.Errorf("%w: %v", ErrServerUnreachable, err)
	}
	defer resp.Body.Close()
	body, rerr := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		// 5xx / 429 are transient (server busy/rate-limited); 4xx are permanent.
		retry := resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests
		return "", retry, fmt.Errorf("inference server returned status %d: %s", resp.StatusCode, string(body))
	}
	// A body cut short mid-stream (client timeout mid-generation, server hangup)
	// otherwise reaches the JSON parser as a truncated document and is misreported
	// as a permanent "malformed completion" — the model's own output blamed for what
	// is really a transient transport failure, so it never gets retried.
	if rerr != nil {
		return "", true, fmt.Errorf("%w: truncated response body after %d bytes: %v", ErrServerUnreachable, len(body), rerr)
	}

	var tokens int
	if c.provider == providerGemini {
		content, tokens, err = parseGemini(body)
	} else {
		content, tokens, err = parseOpenAI(body)
	}
	if err != nil {
		return "", false, err
	}

	c.totalTokens.Add(uint64(tokens))
	if c.Observe != nil {
		c.Observe(purposeOf(sysPrompt), len(sysPrompt)+len(userCtx), len(content), tokens, time.Since(start).Milliseconds())
	}
	return content, false, nil
}

// openaiRequest builds the OpenAI-compatible /v1/chat/completions POST.
func (c *LocalModelClient) openaiRequest(ctx context.Context, sysPrompt, userCtx string) (*http.Request, error) {
	payload, err := json.Marshal(InferenceRequest{
		Model:       c.model,
		Temperature: 0.0, // absolute determinism
		MaxTokens:   4096,
		Messages: []ChatMessage{
			{Role: "system", Content: sysPrompt},
			{Role: "user", Content: userCtx},
		},
		ChatTemplateKwargs: disableThinking,
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	return req, nil
}

// openaiVisionRequest builds a multimodal /v1/chat/completions POST: the user
// message carries an array of content parts — the question plus one image_url per
// frame as a base64 PNG data URI. Verified against the omlx Gemma-4 server.
func (c *LocalModelClient) openaiVisionRequest(ctx context.Context, sysPrompt, question string, images [][]byte) (*http.Request, error) {
	parts := make([]any, 0, len(images)+1)
	parts = append(parts, map[string]any{"type": "text", "text": question})
	for _, img := range images {
		uri := "data:image/png;base64," + base64.StdEncoding.EncodeToString(img)
		parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]any{"url": uri}})
	}
	payload, err := json.Marshal(map[string]any{
		"model":       c.model,
		"temperature": 0.0,
		// Reasoning models (e.g. qwen3.6) spend the early budget on hidden
		// reasoning tokens before any visible content; a tight cap starves the
		// actual answer and returns empty. Give the vision critic room to think
		// AND answer.
		"max_tokens":           2048,
		"chat_template_kwargs": disableThinking,
		"messages": []any{
			map[string]any{"role": "system", "content": sysPrompt},
			map[string]any{"role": "user", "content": parts},
		},
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	return req, nil
}

// parseOpenAI extracts the completion text and token count from an OpenAI-shaped
// response body.
func parseOpenAI(body []byte) (string, int, error) {
	var r InferenceResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return "", 0, fmt.Errorf("malformed completion token array stream: %w", err)
	}
	if len(r.Choices) == 0 {
		return "", 0, fmt.Errorf("no completion choices returned")
	}
	return r.Choices[0].Message.Content, r.Usage.TotalTokens, nil
}

// purposeOf extracts a short label for what a reasoning call is doing, from the
// first meaningful line of its system prompt (e.g. "HDM REFACTORING CORE",
// "GENESIS SYNTHESIS", "acceptance-test auditor").
func purposeOf(sysPrompt string) string {
	line := sysPrompt
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, "SYSTEM ROLE:")
	line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "HDM"))
	if len(line) > 48 {
		line = line[:48] + "…"
	}
	if line == "" {
		return "reasoning"
	}
	return line
}

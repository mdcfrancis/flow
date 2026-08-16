package inference

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// ToolDef describes a tool the model may call (the OpenAI function shape). Parameters is
// a JSON-Schema object for the tool's arguments.
type ToolDef struct {
	Name        string
	Description string
	Parameters  map[string]any
}

// ToolExec executes a tool call by name with the model's raw JSON arguments and returns
// a text result to feed back to the model. Implemented in Go by the caller (the sieve).
type ToolExec func(name, argsJSON string) string

// toolCall / respMessage / toolsResponse mirror the OpenAI chat-completions tool-use
// wire shapes we parse.
type toolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type respMessage struct {
	Role      string     `json:"role"`
	Content   string     `json:"content"`
	ToolCalls []toolCall `json:"tool_calls,omitempty"`
}

type toolsResponse struct {
	Choices []struct {
		Message respMessage `json:"message"`
	} `json:"choices"`
	Usage struct {
		TotalTokens int `json:"total_tokens"`
	} `json:"usage"`
}

// SupportsTools reports whether this backend can run the client-side tool loop. The
// Gemini generateContent path does NOT (we don't send its function-calling schema), so
// InvokeTools degrades to a plain completion for it — and, crucially, a caller must not
// give Gemini a tool-USING preamble: told to call a tool that isn't declared, Gemini
// emits a MALFORMED_FUNCTION_CALL and returns EMPTY. Callers check this to pick a
// direct-authoring preamble instead.
func (c *LocalModelClient) SupportsTools() bool { return c.provider != providerGemini }

// InvokeTools runs a CLIENT-SIDE agentic loop: it offers the model the given tools on
// /v1/chat/completions and, while the model responds with tool_calls, executes each via
// exec (a Go callback) and feeds the results back — up to maxSteps rounds — then returns
// the model's final text. The whole loop is orchestrated here in Go (we do not use any
// server-side agent), so control and verification stay on our side. Gemini and the
// no-tools case fall back to a plain completion; so does a server that rejects tools.
func (c *LocalModelClient) InvokeTools(ctx context.Context, sysPrompt, userCtx string, tools []ToolDef, exec ToolExec, maxSteps int) (string, error) {
	if c.provider == providerGemini || len(tools) == 0 {
		return c.InvokeReasoning(ctx, sysPrompt, userCtx)
	}
	if maxSteps < 1 {
		maxSteps = 6
	}
	toolsPayload := make([]any, len(tools))
	for i, t := range tools {
		toolsPayload[i] = map[string]any{"type": "function", "function": map[string]any{
			"name": t.Name, "description": t.Description, "parameters": t.Parameters}}
	}
	messages := []any{
		map[string]any{"role": "system", "content": sysPrompt},
		map[string]any{"role": "user", "content": userCtx},
	}
	for step := 0; step < maxSteps; step++ {
		msg, err := c.toolRound(ctx, messages, toolsPayload)
		if err != nil {
			if step == 0 && toolsUnsupported(err) {
				return c.InvokeReasoning(ctx, sysPrompt, userCtx) // server can't do tools — degrade gracefully
			}
			return "", err
		}
		if len(msg.ToolCalls) == 0 {
			return msg.Content, nil // the model answered without a tool call — done
		}
		messages = append(messages, map[string]any{"role": "assistant", "content": msg.Content, "tool_calls": msg.ToolCalls})
		for _, tc := range msg.ToolCalls {
			result := exec(tc.Function.Name, tc.Function.Arguments)
			messages = append(messages, map[string]any{"role": "tool", "tool_call_id": tc.ID, "content": result})
		}
	}
	// Out of steps: one last call WITHOUT tools to force a final answer.
	msg, err := c.toolRound(ctx, messages, nil)
	if err != nil {
		return "", err
	}
	return msg.Content, nil
}

// toolRound issues one chat-completions request (with the reconnect/retry window) and
// returns the assistant message (content + any tool_calls).
func (c *LocalModelClient) toolRound(ctx context.Context, messages, tools []any) (respMessage, error) {
	body := map[string]any{"model": c.model, "temperature": 0.0, "max_tokens": 4096, "messages": messages, "chat_template_kwargs": disableThinking}
	if len(tools) > 0 {
		body["tools"] = tools
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return respMessage{}, err
	}
	var out respMessage
	_, err = c.retryLoop(ctx, func(ctx context.Context) (string, bool, error) {
		req, e := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/chat/completions", bytes.NewReader(payload))
		if e != nil {
			return "", false, e
		}
		req.Header.Set("Content-Type", "application/json")
		if c.apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+c.apiKey)
		}
		resp, e := c.client.Do(req)
		if e != nil {
			return "", true, fmt.Errorf("%w: %v", ErrServerUnreachable, e)
		}
		defer resp.Body.Close()
		b, rerr := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			retry := resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests
			return "", retry, fmt.Errorf("tools request status %d: %s", resp.StatusCode, string(b))
		}
		if rerr != nil {
			return "", true, fmt.Errorf("%w: truncated tools response after %d bytes: %v", ErrServerUnreachable, len(b), rerr)
		}
		var tr toolsResponse
		if e := json.Unmarshal(b, &tr); e != nil || len(tr.Choices) == 0 {
			return "", false, fmt.Errorf("bad tools response: %v", e)
		}
		out = tr.Choices[0].Message
		c.totalTokens.Add(uint64(tr.Usage.TotalTokens))
		return "", false, nil
	})
	return out, err
}

// toolsUnsupported guesses whether an error is the server rejecting tool use (a 4xx
// mentioning tools/functions), so InvokeTools can fall back to a plain completion.
func toolsUnsupported(err error) bool {
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "status 4") && (strings.Contains(s, "tool") || strings.Contains(s, "function") || strings.Contains(s, "not supported") || strings.Contains(s, "unsupported"))
}

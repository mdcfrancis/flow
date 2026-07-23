package inference

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestInvokeToolsLoop proves the client-side agentic loop end-to-end against a scripted
// server: round 1 returns a tool_call, the Go exec runs, round 2 returns the final text.
func TestInvokeToolsLoop(t *testing.T) {
	round := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		round++
		w.Header().Set("Content-Type", "application/json")
		if round == 1 {
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"",` +
				`"tool_calls":[{"id":"c1","type":"function","function":{"name":"compile_check","arguments":"{\"wat\":\"(module)\"}"}}]}}],` +
				`"usage":{"total_tokens":10}}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"(module (func))"}}],"usage":{"total_tokens":5}}`))
	}))
	defer srv.Close()

	c := NewLocalModelClient(srv.URL, "m")
	var seen []string
	exec := func(name, args string) string {
		seen = append(seen, name+" "+args)
		return "ok: compiled"
	}
	out, err := c.InvokeTools(context.Background(), "sys", "build a cell",
		[]ToolDef{{Name: "compile_check", Description: "compile WAT", Parameters: map[string]any{"type": "object"}}}, exec, 6)
	if err != nil {
		t.Fatalf("InvokeTools: %v", err)
	}
	if out != "(module (func))" {
		t.Fatalf("final answer = %q", out)
	}
	if len(seen) != 1 || !strings.HasPrefix(seen[0], "compile_check ") || !strings.Contains(seen[0], "(module)") {
		t.Fatalf("tool not executed with args: %v", seen)
	}
	if c.TotalTokens() != 15 { // 10 (round 1) + 5 (round 2)
		t.Fatalf("token accounting = %d, want 15", c.TotalTokens())
	}
	if round != 2 {
		t.Fatalf("expected 2 rounds, got %d", round)
	}
}

// TestInvokeToolsFallback: a server that rejects tools falls back to a plain completion.
func TestInvokeToolsFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(body)
		if strings.Contains(string(body), `"tools"`) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"tools are not supported by this model"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"plain answer"}}],"usage":{"total_tokens":3}}`))
	}))
	defer srv.Close()

	c := NewLocalModelClient(srv.URL, "m")
	c.reconnect = 0 // don't retry the 400 in this test
	out, err := c.InvokeTools(context.Background(), "sys", "user",
		[]ToolDef{{Name: "x", Parameters: map[string]any{"type": "object"}}}, func(string, string) string { return "" }, 4)
	if err != nil {
		t.Fatalf("fallback errored: %v", err)
	}
	if out != "plain answer" {
		t.Fatalf("fallback answer = %q", out)
	}
}

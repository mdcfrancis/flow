package inference

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGeminiClientRoundTrip(t *testing.T) {
	var gotPath, gotKey string
	var gotReq geminiRequestBody
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotKey = r.Header.Get("x-goog-api-key")
		_ = json.NewDecoder(r.Body).Decode(&gotReq)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"(module "},{"text":"here)"}],"role":"model"},"finishReason":"STOP"}],"usageMetadata":{"totalTokenCount":42}}`))
	}))
	defer srv.Close()

	c := NewGeminiClient(srv.URL, "gemini-3.1-flash-lite", "KEY123")
	out, err := c.InvokeReasoning(context.Background(), "SYS", "USR")
	if err != nil {
		t.Fatalf("InvokeReasoning: %v", err)
	}
	if out != "(module here)" { // parts concatenated
		t.Fatalf("content = %q, want concatenated parts", out)
	}
	if gotPath != "/v1beta/models/gemini-3.1-flash-lite:generateContent" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotKey != "KEY123" {
		t.Fatalf("api key header = %q, want KEY123", gotKey)
	}
	if gotReq.SystemInstruction == nil || len(gotReq.SystemInstruction.Parts) == 0 || gotReq.SystemInstruction.Parts[0].Text != "SYS" {
		t.Fatalf("system prompt not mapped to systemInstruction: %+v", gotReq.SystemInstruction)
	}
	if len(gotReq.Contents) == 0 || gotReq.Contents[0].Parts[0].Text != "USR" {
		t.Fatalf("user context not mapped to contents: %+v", gotReq.Contents)
	}
	if c.TotalTokens() != 42 {
		t.Fatalf("tokens = %d, want 42", c.TotalTokens())
	}
}

func TestGeminiSafetyBlockIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"candidates":[],"promptFeedback":{"blockReason":"SAFETY"}}`))
	}))
	defer srv.Close()
	c := NewGeminiClient(srv.URL, "m", "k")
	if _, err := c.InvokeReasoning(context.Background(), "s", "u"); err == nil {
		t.Fatal("expected an error when Gemini returns no candidates")
	}
}

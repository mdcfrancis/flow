package inference

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// TestOutageTaggedUnreachable verifies a connection-level failure is tagged
// ErrServerUnreachable, so callers can tell an infra outage apart from a genuine
// synthesis failure (and not park a cell over a blip).
func TestOutageTaggedUnreachable(t *testing.T) {
	c := NewLocalModelClient("http://127.0.0.1:1", "test") // nothing listening
	c.reconnect = 0                                        // fail fast, no retries
	_, err := c.InvokeReasoning(context.Background(), "sys", "usr")
	if err == nil {
		t.Fatal("expected an error against a dead endpoint")
	}
	if !errors.Is(err, ErrServerUnreachable) {
		t.Fatalf("error not tagged ErrServerUnreachable: %v", err)
	}
}

// TestInvokeReasoningReconnects verifies a transient outage (the server failing
// the first few requests, as if restarting) is retried and eventually succeeds.
func TestInvokeReasoningReconnects(t *testing.T) {
	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt64(&hits, 1) <= 2 {
			http.Error(w, "warming up", http.StatusServiceUnavailable) // 503 → retriable
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"total_tokens":3}}`))
	}))
	defer srv.Close()

	c := NewLocalModelClient(srv.URL, "test")
	c.reconnect = 30 * time.Second // enough to cover the ~1+2s backoff
	out, err := c.InvokeReasoning(context.Background(), "sys", "usr")
	if err != nil {
		t.Fatalf("reconnect should have recovered: %v", err)
	}
	if out != "ok" {
		t.Fatalf("content = %q, want ok", out)
	}
	if got := atomic.LoadInt64(&hits); got != 3 {
		t.Fatalf("hits = %d, want 3 (2 transient failures + 1 success)", got)
	}
}

// TestInvokeReasoningFailsFastOnPermanent verifies a 4xx (auth/bad request) is
// NOT retried — retrying can't fix it, so it returns immediately.
func TestInvokeReasoningFailsFastOnPermanent(t *testing.T) {
	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		http.Error(w, "unauthorized", http.StatusUnauthorized) // 401 → permanent
	}))
	defer srv.Close()

	c := NewLocalModelClient(srv.URL, "test")
	c.reconnect = 30 * time.Second
	if _, err := c.InvokeReasoning(context.Background(), "sys", "usr"); err == nil {
		t.Fatal("expected an error on 401")
	}
	if got := atomic.LoadInt64(&hits); got != 1 {
		t.Fatalf("hits = %d, want 1 (no retry on 4xx)", got)
	}
}

// TestInvokeReasoningRespectsCtxCancel verifies a cancelled context stops the
// retry wait promptly instead of blocking for the whole reconnect window.
func TestInvokeReasoningRespectsCtxCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	c := NewLocalModelClient(srv.URL, "test")
	c.reconnect = 10 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := c.InvokeReasoning(ctx, "sys", "usr"); err == nil {
		t.Fatal("expected an error")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("ctx cancel not honored: blocked %s", elapsed)
	}
}

package integration

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"testing"
	"time"
)

// serveOnFreePort starts the edge services on an ephemeral port and returns its
// base URL, so a route can be exercised end-to-end through the real mux.
func serveOnFreePort(t *testing.T, s Services) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	srv := Serve(ctx, addr, s)
	t.Cleanup(func() { _ = srv.Close() })

	base := "http://" + addr
	// Wait for the listener to accept before the test drives it.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := net.DialTimeout("tcp", addr, 100*time.Millisecond); err == nil {
			_ = c.Close()
			return base
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("server never came up on %s", addr)
	return ""
}

func TestLogResetRoute(t *testing.T) {
	lines := []string{"a", "b", "c"}
	cleared := 0
	base := serveOnFreePort(t, Services{
		Log: func() []string { return lines },
		LogReset: func() int {
			n := len(lines)
			lines = nil
			cleared += n
			return n
		},
	})

	// A GET must NOT clear: the console probes the route with one to decide whether
	// to show the control, and a page load must never wipe the operator's log.
	resp, err := http.Get(base + "/log/reset")
	if err != nil {
		t.Fatalf("probe GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET /log/reset = %d, want 405", resp.StatusCode)
	}
	if cleared != 0 {
		t.Fatalf("GET cleared %d lines; it must be side-effect free", cleared)
	}

	// POST clears and reports the count.
	resp2, err := http.Post(base+"/log/reset", "", nil)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("POST /log/reset = %d, want 200", resp2.StatusCode)
	}
	var out struct {
		Cleared int `json:"cleared"`
	}
	if err := json.NewDecoder(resp2.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Cleared != 3 {
		t.Fatalf("cleared = %d, want 3", out.Cleared)
	}
	if len(lines) != 0 {
		t.Fatalf("log not cleared: %v", lines)
	}
}

// With no LogReset wired the route must be absent entirely, so the console's
// probe sees 404 and hides the control rather than showing a dead button.
func TestLogResetRouteAbsentWhenUnwired(t *testing.T) {
	base := serveOnFreePort(t, Services{Log: func() []string { return []string{"a"} }})
	resp, err := http.Get(base + "/log/reset")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET /log/reset = %d with no LogReset wired, want 404", resp.StatusCode)
	}
}

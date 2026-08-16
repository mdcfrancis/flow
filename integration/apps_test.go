package integration

import (
	"encoding/json"
	"net/http"
	"testing"
)

// Retire deletes an application permanently, so the route must refuse anything
// that could reach it by accident: a GET (a prefetch or a refresh), or a POST
// that does not name what it means to destroy.
func TestAppRetireRouteGuards(t *testing.T) {
	called := []string{}
	base := serveOnFreePort(t, Services{
		Apps: func() any { return []any{} },
		AppRetire: func(ns string) (int, error) {
			called = append(called, ns)
			return 7, nil
		},
	})

	resp, err := http.Get(base + "/app/retire?ns=urn:hdm:apps:demo")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET /app/retire = %d, want 405", resp.StatusCode)
	}

	resp2, err := http.Post(base+"/app/retire", "", nil) // no ns
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusBadRequest {
		t.Fatalf("POST with no ns = %d, want 400", resp2.StatusCode)
	}

	if len(called) != 0 {
		t.Fatalf("retire ran %v; neither a GET nor a namespace-less POST may delete anything", called)
	}

	resp3, err := http.Post(base+"/app/retire?ns=urn:hdm:apps:demo", "", nil)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp3.Body.Close()
	if resp3.StatusCode != http.StatusOK {
		t.Fatalf("valid POST = %d, want 200", resp3.StatusCode)
	}
	var out struct {
		Retired string `json:"retired"`
		Refs    int    `json:"refs"`
	}
	if err := json.NewDecoder(resp3.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Retired != "urn:hdm:apps:demo" || out.Refs != 7 {
		t.Fatalf("got %+v", out)
	}
	if len(called) != 1 || called[0] != "urn:hdm:apps:demo" {
		t.Fatalf("retire called with %v", called)
	}
}

// With no AppRetire wired the route must be absent, so the console's probe hides
// the control instead of showing a dead button.
func TestAppRoutesAbsentWhenUnwired(t *testing.T) {
	base := serveOnFreePort(t, Services{})
	for _, path := range []string{"/apps", "/app/retire"} {
		resp, err := http.Get(base + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s = %d with nothing wired, want 404", path, resp.StatusCode)
		}
	}
}

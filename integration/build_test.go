package integration

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mdcfrancis/flow/appgen"
	"github.com/mdcfrancis/flow/evolution"
	"github.com/mdcfrancis/flow/storage"
)

// growModel returns an app envelope on the first call and a valid genesis
// skeleton on all subsequent calls (WIT/genesis/acceptance).
type growModel struct {
	envelope string
	calls    int
}

func (m *growModel) InvokeReasoning(ctx context.Context, sys, user string) (string, error) {
	m.calls++
	if m.calls == 1 {
		return m.envelope, nil
	}
	return `(module (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 100))
	  (func (export "run-tick") (param i32 i32) (result i32) i32.const 0))`, nil
}

func TestBuildServerGrowsAndEnrolls(t *testing.T) {
	le, err := storage.NewLedgerEngine(filepath.Join(t.TempDir(), "hdm.db"))
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	defer le.Close()

	env := `{"application_namespace":"urn:hdm:apps:life",
	  "subsystem_requirements":[
	    {"identity":"urn:hdm:apps:life:grid","semantics":"maintain the cell grid"},
	    {"identity":"urn:hdm:apps:life:step","semantics":"advance one generation"}]}`
	grower := appgen.NewGrower(le, &growModel{envelope: env})
	registry := evolution.NewCellRegistry("urn:hdm:sys:optimizer")
	bs := NewBuildServer(grower, registry, nil)

	rec := httptest.NewRecorder()
	bs.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/build", strings.NewReader("build a game of life")))
	if rec.Code != http.StatusOK {
		t.Fatalf("build status = %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "urn:hdm:apps:life") {
		t.Fatalf("response missing namespace: %s", rec.Body.String())
	}

	// Scaffolding runs in the background; both subsystems must get enrolled in
	// the annealing registry (poll until they appear).
	enrolled := func() (bool, bool) {
		var grid, step bool
		for _, u := range registry.List() {
			grid = grid || u == "urn:hdm:apps:life:grid"
			step = step || u == "urn:hdm:apps:life:step"
		}
		return grid, step
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		if g, s := enrolled(); g && s {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("subsystems not enrolled in time: %v", registry.List())
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Empty objective is rejected.
	rec2 := httptest.NewRecorder()
	bs.ServeHTTP(rec2, httptest.NewRequest(http.MethodPost, "/build", strings.NewReader("  ")))
	if rec2.Code != http.StatusBadRequest {
		t.Fatalf("empty objective status = %d, want 400", rec2.Code)
	}
}

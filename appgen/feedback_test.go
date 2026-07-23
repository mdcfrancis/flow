package appgen

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mdcfrancis/flow/evolution"
	"github.com/mdcfrancis/flow/storage"
)

type feedbackModel struct{ out string }

func (m feedbackModel) InvokeReasoning(ctx context.Context, sys, user string) (string, error) {
	return m.out, nil
}

func TestRouteFeedback(t *testing.T) {
	le, err := storage.NewLedgerEngine(filepath.Join(t.TempDir(), "hdm.db"))
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	defer le.Close()
	ns := "urn:hdm:apps:mandelbrot"
	g := NewGrower(le, feedbackModel{out: `{"guidance":[
	  {"scope":"system","statement":"map values to a high-contrast palette"},
	  {"scope":"app","statement":"the interior of the set should be pure black"}
	]}`})
	routed, err := g.RouteFeedback(context.Background(), ns, "colors are dim and the middle isn't black")
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if len(routed) != 2 {
		t.Fatalf("expected 2 routed, got %d", len(routed))
	}
	sys := evolution.LoadGuidance(le, evolution.SystemGuidanceKey)
	if len(sys.Entries) != 1 || sys.Entries[0].Statement != "map values to a high-contrast palette" {
		t.Errorf("system guidance wrong: %+v", sys.Entries)
	}
	app := evolution.LoadGuidance(le, evolution.AppGuidanceKey(ns))
	if len(app.Entries) != 1 || app.Entries[0].Statement != "the interior of the set should be pure black" {
		t.Errorf("app guidance wrong: %+v", app.Entries)
	}
	// With no focused app, app-scoped items fall back to system.
	g2 := NewGrower(le, feedbackModel{out: `{"guidance":[{"scope":"app","statement":"be faster"}]}`})
	if _, err := g2.RouteFeedback(context.Background(), "", "make it faster"); err != nil {
		t.Fatalf("route no-ns: %v", err)
	}
	if r := evolution.LoadGuidance(le, evolution.SystemGuidanceKey); len(r.Entries) != 2 {
		t.Errorf("app-scoped with no namespace should route to system: %d entries", len(r.Entries))
	}
}

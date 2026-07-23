package appgen

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mdcfrancis/flow/evolution"
	"github.com/mdcfrancis/flow/storage"
)

func TestAutoOptimizePrompts(t *testing.T) {
	le, _ := storage.NewLedgerEngine(filepath.Join(t.TempDir(), "hdm.db"))
	defer le.Close()
	// Below threshold → nothing happens.
	evolution.AddPromptGrievance(le, "feedback", "bad json once")
	g := NewGrower(le, &seqReasoner{outs: []string{"REVISED", `{"safe":true,"reason":"ok"}`}})
	if n := g.AutoOptimizePrompts(context.Background()); n != 0 {
		t.Fatalf("below threshold should not optimize, got %d", n)
	}
	// Cross the threshold → auto-refine + clear grievances.
	evolution.AddPromptGrievance(le, "feedback", "bad json two")
	evolution.AddPromptGrievance(le, "feedback", "bad json three")
	if n := g.AutoOptimizePrompts(context.Background()); n != 1 {
		t.Fatalf("threshold reached should optimize 1, got %d", n)
	}
	if len(evolution.LoadPromptGrievances(le, "feedback")) != 0 {
		t.Error("grievances should be cleared after adoption")
	}
	if evolution.ResolvePrompt(le, "feedback", "DEFAULT") != "REVISED" {
		t.Error("refined prompt should be adopted")
	}
}

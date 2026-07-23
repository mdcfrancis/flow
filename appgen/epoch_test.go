package appgen

import (
	"path/filepath"
	"testing"

	"github.com/mdcfrancis/flow/evolution"
	"github.com/mdcfrancis/flow/storage"
)

func TestEpochSaveRestore(t *testing.T) {
	le, _ := storage.NewLedgerEngine(filepath.Join(t.TempDir(), "hdm.db"))
	defer le.Close()
	g := NewGrower(le, feedbackModel{})
	// Establish a system state: a refined prompt + guidance.
	_ = evolution.SavePromptOverride(le, "feedback", "REFINED FEEDBACK PROMPT")
	gd := &evolution.Guidance{}
	gd.Add("renderers must be high-contrast")
	_ = evolution.SaveGuidance(le, evolution.SystemGuidanceKey, gd)

	if _, err := g.SaveEpoch("v1"); err != nil {
		t.Fatalf("save: %v", err)
	}
	if names := g.ListEpochs(); len(names) != 1 || names[0] != "v1" {
		t.Fatalf("list: %v", names)
	}

	// Drift the system away from the epoch.
	_ = evolution.SavePromptOverride(le, "feedback", "DRIFTED")
	_ = evolution.SavePromptOverride(le, "boundary", "EXTRA OVERRIDE NOT IN EPOCH")
	_ = evolution.SaveGuidance(le, evolution.SystemGuidanceKey, &evolution.Guidance{})

	if _, err := g.RestoreEpoch("v1"); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := evolution.ResolvePrompt(le, "feedback", "DEFAULT"); got != "REFINED FEEDBACK PROMPT" {
		t.Errorf("feedback prompt not restored: %q", got)
	}
	if got := evolution.LoadPromptOverride(le, "boundary"); got != "" {
		t.Errorf("override not in the epoch should be cleared, got %q", got)
	}
	if gd := evolution.LoadGuidance(le, evolution.SystemGuidanceKey); len(gd.Entries) != 1 {
		t.Errorf("guidance not restored: %d entries", len(gd.Entries))
	}
}

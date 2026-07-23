package evolution

import (
	"path/filepath"
	"testing"

	"github.com/mdcfrancis/flow/storage"
)

func TestPromptOverride(t *testing.T) {
	le, err := storage.NewLedgerEngine(filepath.Join(t.TempDir(), "hdm.db"))
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	defer le.Close()
	if got := ResolvePrompt(le, "feedback", "DEFAULT"); got != "DEFAULT" {
		t.Errorf("no override should return default, got %q", got)
	}
	if err := SavePromptOverride(le, "feedback", "REFINED"); err != nil {
		t.Fatalf("save: %v", err)
	}
	if got := ResolvePrompt(le, "feedback", "DEFAULT"); got != "REFINED" {
		t.Errorf("override should win, got %q", got)
	}
	if names := ListPromptOverrides(le); len(names) != 1 || names[0] != "feedback" {
		t.Errorf("list wrong: %v", names)
	}
	if err := SavePromptOverride(le, "feedback", ""); err != nil { // clear reverts to default
		t.Fatalf("clear: %v", err)
	}
	if got := ResolvePrompt(le, "feedback", "DEFAULT"); got != "DEFAULT" {
		t.Errorf("cleared override should revert to default, got %q", got)
	}
}

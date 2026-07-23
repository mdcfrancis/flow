package appgen

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mdcfrancis/flow/evolution"
	"github.com/mdcfrancis/flow/storage"
)

// seqReasoner returns scripted responses in order (optimizer call, then validator call).
type seqReasoner struct {
	outs []string
	i    int
}

func (m *seqReasoner) InvokeReasoning(ctx context.Context, sys, user string) (string, error) {
	o := m.outs[m.i%len(m.outs)]
	m.i++
	return o, nil
}

func TestOptimizePromptAdoptsWhenSafe(t *testing.T) {
	le, _ := storage.NewLedgerEngine(filepath.Join(t.TempDir(), "hdm.db"))
	defer le.Close()
	g := NewGrower(le, &seqReasoner{outs: []string{
		"REVISED PROMPT TEXT",                       // optimizer
		`{"safe": true, "reason": "same contract"}`, // validator
	}})
	rev, adopted, reason, err := g.OptimizePrompt(context.Background(), "feedback", "CURRENT PROMPT", "be clearer")
	if err != nil {
		t.Fatalf("optimize: %v", err)
	}
	if !adopted || rev != "REVISED PROMPT TEXT" {
		t.Fatalf("expected adoption: adopted=%v rev=%q reason=%q", adopted, rev, reason)
	}
	if got := evolution.ResolvePrompt(le, "feedback", "CURRENT PROMPT"); got != "REVISED PROMPT TEXT" {
		t.Errorf("override not persisted: %q", got)
	}
}

func TestOptimizePromptKeepsDefaultWhenUnsafe(t *testing.T) {
	le, _ := storage.NewLedgerEngine(filepath.Join(t.TempDir(), "hdm.db"))
	defer le.Close()
	g := NewGrower(le, &seqReasoner{outs: []string{
		"REVISED THAT DROPS THE JSON CONTRACT",
		`{"safe": false, "reason": "drops the required JSON output"}`,
	}})
	_, adopted, _, err := g.OptimizePrompt(context.Background(), "feedback", "CURRENT PROMPT", "be clearer")
	if err != nil {
		t.Fatalf("optimize: %v", err)
	}
	if adopted {
		t.Error("unsafe revision must NOT be adopted")
	}
	if got := evolution.ResolvePrompt(le, "feedback", "CURRENT PROMPT"); got != "CURRENT PROMPT" {
		t.Errorf("should keep default when unsafe, got %q", got)
	}
}

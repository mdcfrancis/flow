package appgen

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mdcfrancis/flow/evolution"
	"github.com/mdcfrancis/flow/storage"
)

func TestOptimizePolicyClamps(t *testing.T) {
	le, _ := storage.NewLedgerEngine(filepath.Join(t.TempDir(), "hdm.db"))
	defer le.Close()
	// Model proposes a more-persistent, tighter-critic policy — but an out-of-bounds interval.
	g := NewGrower(le, feedbackModel{out: `{"maxStallRetries":6,"grievanceThreshold":2,"visionIntervalSec":1,"codeCriticIntervalSec":90}`})
	applied, changed, _, err := g.OptimizePolicy(context.Background(), "cells give up too early; catch regressions faster")
	if err != nil {
		t.Fatalf("optimize: %v", err)
	}
	if !changed {
		t.Fatal("expected a policy change")
	}
	if applied.MaxStallRetries != 6 || applied.GrievanceThreshold != 2 || applied.CodeCriticIntervalSec != 90 {
		t.Errorf("applied wrong: %+v", applied)
	}
	if applied.VisionIntervalSec != 15 { // clamped up from 1 to the floor
		t.Errorf("out-of-bounds vision interval should clamp to 15, got %d", applied.VisionIntervalSec)
	}
	if p := evolution.LoadPolicy(le); p.MaxStallRetries != 6 {
		t.Errorf("not persisted: %+v", p)
	}
}

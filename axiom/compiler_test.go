package axiom

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mdcfrancis/flow/manifest"
	"github.com/mdcfrancis/flow/storage"
)

type fakeModel struct{ resp string }

func (f fakeModel) InvokeReasoning(ctx context.Context, sys, user string) (string, error) {
	return f.resp, nil
}

func TestCompileAndApply(t *testing.T) {
	le, err := storage.NewLedgerEngine(filepath.Join(t.TempDir(), "hdm.db"))
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	defer le.Close()

	// The model returns a JSON target wrapped in prose + a fence to exercise
	// extraction.
	resp := "Here is the axiom:\n```json\n" +
		`{"intent":"validate signatures","domain_tags":["auth","billing"],"saliency":0.8,"assertions":["sig must verify"]}` +
		"\n```\n"
	c := NewCompiler(le, fakeModel{resp: resp})

	tgt, err := c.Compile(context.Background(), "Ensure inbound transactions validate signatures")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if tgt.Saliency != 0.8 || len(tgt.DomainTags) != 2 || tgt.ID == "" {
		t.Fatalf("bad target: %+v", tgt)
	}

	set, _ := c.Load()
	if len(set.Targets) != 1 {
		t.Fatalf("target set size = %d, want 1", len(set.Targets))
	}
	if s := set.SaliencyFor([]string{"auth"}); s != 0.8 {
		t.Fatalf("SaliencyFor(auth) = %v, want 0.8", s)
	}
	if s := set.SaliencyFor([]string{"unrelated"}); s != 0 {
		t.Fatalf("SaliencyFor(unrelated) = %v, want 0", s)
	}

	// Re-compiling the same intent is idempotent (no duplicate).
	if _, err := c.Compile(context.Background(), "Ensure inbound transactions validate signatures"); err != nil {
		t.Fatalf("recompile: %v", err)
	}
	set, _ = c.Load()
	if len(set.Targets) != 1 {
		t.Fatalf("after recompile target set size = %d, want 1", len(set.Targets))
	}

	// ApplyTo propagates saliency into a matching cell's descriptor.
	repo := manifest.NewRepository(le)
	h, _, _ := repo.PutCell("urn:hdm:cell:auth", "(module)", []byte{0}, manifest.SemanticManifest{DomainTags: []string{"auth"}}, 0)
	repo.SeedRef("urn:hdm:cell:auth", h)
	if err := c.ApplyTo(repo, []string{"urn:hdm:cell:auth"}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	desc, _ := repo.Load("urn:hdm:cell:auth")
	if desc.Saliency != 0.8 {
		t.Fatalf("cell saliency = %v after ApplyTo, want 0.8", desc.Saliency)
	}
}

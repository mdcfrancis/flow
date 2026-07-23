package appgen

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/mdcfrancis/flow/storage"
)

type diffModel struct {
	out string
	err error
}

func (m diffModel) InvokeReasoning(ctx context.Context, sys, user string) (string, error) {
	return m.out, m.err
}

func TestJudgeDifficulty(t *testing.T) {
	le, _ := storage.NewLedgerEngine(filepath.Join(t.TempDir(), "hdm.db"))
	defer le.Close()

	// Not doable -> (false, reason).
	g := NewGrower(le, diffModel{out: `{"doable":false,"reason":"bundles init + integrate + pairwise collisions"}`})
	if doable, r := g.JudgeDifficulty(context.Background(), "physics", "urn:hdm:apps:x"); doable || r == "" {
		t.Fatalf("hard subsystem must be not-doable with a reason, got doable=%v r=%q", doable, r)
	}

	// Doable -> true.
	g = NewGrower(le, diffModel{out: `{"doable":true,"reason":"a single loop"}`})
	if doable, _ := g.JudgeDifficulty(context.Background(), "render one ball", "urn:hdm:apps:x"); !doable {
		t.Fatal("simple subsystem must be doable")
	}

	// FAIL-OPEN: a model error must NOT over-decompose (assume doable).
	g = NewGrower(le, diffModel{err: errors.New("model down")})
	if doable, _ := g.JudgeDifficulty(context.Background(), "physics", "urn:hdm:apps:x"); !doable {
		t.Fatal("a model fault must fail open to doable (reactive fracture is the backstop)")
	}

	// Garbage output -> fail open.
	g = NewGrower(le, diffModel{out: "not json"})
	if doable, _ := g.JudgeDifficulty(context.Background(), "physics", "urn:hdm:apps:x"); !doable {
		t.Fatal("unparseable output must fail open to doable")
	}
}

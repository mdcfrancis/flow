package evolution

import (
	"path/filepath"
	"testing"

	"github.com/mdcfrancis/flow/storage"
)

func kbLedger(t *testing.T) *storage.LedgerEngine {
	t.Helper()
	le, err := storage.NewLedgerEngine(filepath.Join(t.TempDir(), "hdm.db"))
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	t.Cleanup(func() { le.Close() })
	return le
}

func TestExampleStoreRoundTripAndNovelty(t *testing.T) {
	le := kbLedger(t)
	if len(LoadExamples(le)) != 0 {
		t.Fatal("empty store expected")
	}

	drawFixed := Example{Kind: "render", Entry: "render-frame", Semantics: "draw a circle",
		Tags: []string{"draw-at-position"}, WAT: "(module (func (export \"render-frame\")))", Score: "3/5", Provenance: "seed"}
	if ok, _ := AddExample(le, drawFixed); !ok {
		t.Fatal("first example should be stored")
	}
	if got := LoadExamples(le); len(got) != 1 || got[0].ID == "" {
		t.Fatalf("round-trip failed: %+v", got)
	}

	// Same id → not re-stored.
	if ok, _ := AddExample(le, drawFixed); ok {
		t.Fatal("identical example must not be stored twice")
	}

	// Same shape (render + draw-at-position), WORSE score → rejected.
	worse := drawFixed
	worse.WAT = "(module (func (export \"render-frame\")) (; padding for a longer body ;))"
	worse.Score = "2/5"
	if ok, _ := AddExample(le, worse); ok {
		t.Fatal("worse example for a covered shape must be rejected")
	}

	// Same shape, BETTER score → replaces.
	better := drawFixed
	better.WAT = "(module (func (export \"render-frame\")) (; better ;))"
	better.Score = "5/5"
	if ok, _ := AddExample(le, better); !ok {
		t.Fatal("better example should replace")
	}
	if got := LoadExamples(le); len(got) != 1 || got[0].Score != "5/5" {
		t.Fatalf("store should hold the better one: %+v", got)
	}

	// Different shape (compute) → additive.
	if ok, _ := AddExample(le, Example{Kind: "compute", Entry: "run-tick", Semantics: "integrate + bounce",
		Tags: []string{"wall-bounce"}, WAT: "(module (func (export \"run-tick\")))", Score: "4/4"}); !ok {
		t.Fatal("new-shape example should be additive")
	}
	if len(LoadExamples(le)) != 2 {
		t.Fatalf("expected 2 examples, got %d", len(LoadExamples(le)))
	}
}

func TestExampleRetrievalKindFilterAndRanking(t *testing.T) {
	le := kbLedger(t)
	_, _ = AddExample(le, Example{Kind: "render", Entry: "render-frame", Semantics: "read ball_x ball_y and draw a circle there",
		Tags: []string{"draw-at-position"}, WAT: "R1", Score: "5/5"})
	_, _ = AddExample(le, Example{Kind: "render", Entry: "render-frame", Semantics: "draw a static background grid",
		Tags: []string{"static-draw"}, WAT: "R2", Score: "5/5"})
	_, _ = AddExample(le, Example{Kind: "compute", Entry: "run-tick", Semantics: "integrate position and bounce off walls",
		Tags: []string{"wall-bounce"}, WAT: "C1", Score: "4/4"})

	// A render target must NOT see the compute example (hard kind filter).
	got := FindExamples(le, "render", "", "draw the ball at its position", []string{"ball_x", "ball_y"}, nil, 5)
	for _, e := range got {
		if e.Kind != "render" {
			t.Fatalf("kind filter leaked a %s example", e.Kind)
		}
	}
	// The position-draw example should rank first for a draw-at-position intent.
	if len(got) == 0 || got[0].WAT != "R1" {
		t.Fatalf("expected R1 (draw-at-position) first, got %+v", got)
	}
}

func TestDocumentStoreAndRetrieval(t *testing.T) {
	le := kbLedger(t)
	if err := AddDocument(le, Document{Topic: "draw-at-position", Title: "Reading a field and drawing there",
		Kinds: []string{"render"}, Body: "Read ball_x and ball_y from the contract offsets, then emit a circle record at those coordinates.", Provenance: "seed"}); err != nil {
		t.Fatal(err)
	}
	if err := AddDocument(le, Document{Topic: "wall-bounce", Title: "Reflecting velocity at a boundary",
		Kinds: []string{"compute"}, Body: "Add velocity to position; if it crosses 0 or the screen bound, negate the velocity and clamp.", Provenance: "seed"}); err != nil {
		t.Fatal(err)
	}
	// Topic-keyed replace.
	if err := AddDocument(le, Document{Topic: "draw-at-position", Title: "v2", Kinds: []string{"render"}, Body: "updated", Provenance: "seed"}); err != nil {
		t.Fatal(err)
	}
	if ds := LoadDocuments(le); len(ds) != 2 {
		t.Fatalf("topic replace failed: %d docs", len(ds))
	}
	// A render target's draw intent surfaces the draw-at-position doc, not the compute one.
	got := FindDocuments(le, "render", "draw the ball at its coordinates", 5)
	if len(got) == 0 || got[0].Topic != "draw-at-position" {
		t.Fatalf("expected draw-at-position doc first, got %+v", got)
	}
	for _, d := range got {
		if d.Topic == "wall-bounce" {
			t.Fatal("compute-only doc leaked to a render target")
		}
	}
}

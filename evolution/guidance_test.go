package evolution

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/mdcfrancis/flow/storage"
)

func TestGuidanceStore(t *testing.T) {
	le, err := storage.NewLedgerEngine(filepath.Join(t.TempDir(), "hdm.db"))
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	defer le.Close()
	g := LoadGuidance(le, SystemGuidanceKey)
	if len(g.Entries) != 0 {
		t.Fatal("expected empty")
	}
	if _, isNew := g.Add("renderers must use a high-contrast palette"); !isNew {
		t.Error("first add should be new")
	}
	if _, isNew := g.Add("Renderers Must Use A High-Contrast Palette"); isNew {
		t.Error("case-different duplicate should not be new")
	}
	e2, _ := g.Add("coordinate through shared state")
	if err := SaveGuidance(le, SystemGuidanceKey, g); err != nil {
		t.Fatalf("save: %v", err)
	}
	got := LoadGuidance(le, SystemGuidanceKey)
	if len(got.Entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(got.Entries))
	}
	if r := got.Render("HDR:"); !strings.Contains(r, "high-contrast") || !strings.Contains(r, "- ") {
		t.Errorf("render missing content: %q", r)
	}
	if !got.Remove(e2.ID) || len(got.Entries) != 1 {
		t.Errorf("remove failed: %d entries remain", len(got.Entries))
	}
}

package evolution

import (
	"testing"

	"github.com/mdcfrancis/flow/flux"
)

// A surface defined as DATA round-trips through the ledger and reconstructs a working
// surface — the parser (for the S-expr family) is now ledger-resident data the system
// can hold and evolve, no Go type required.
func TestSurfaceSpecLedgerRoundTrip(t *testing.T) {
	le := newLedger(t)
	if _, ok := LoadSurfaceSpec(le, "terse"); ok {
		t.Fatal("empty store should have no spec")
	}
	spec := flux.SurfaceSpec{
		Name:    "terse",
		Keyword: map[string]string{"cell": "c", "reads": "r", "writes": "w", "write": "!", "let": "lt"},
	}
	if err := SaveSurfaceSpec(le, spec); err != nil {
		t.Fatal(err)
	}
	got, ok := LoadSurfaceSpec(le, "terse")
	if !ok || got.Keyword["cell"] != "c" || got.Keyword["write"] != "!" {
		t.Fatalf("spec did not round-trip: %+v ok=%v", got, ok)
	}

	// Reconstruct a working surface from the ledger data and prove it is behavior-safe.
	surf := flux.SpecSurface{Spec: got}
	layout := flux.Layout{"a": {Type: flux.TInt, Offset: 0xB0000}, "b": {Type: flux.TInt, Offset: 0xB0004, ReadOnly: true}}
	cell, err := flux.SExpr{}.Read("m", `(cell m (reads a b) (writes a) (write (a (+ a b))))`, layout)
	if err != nil {
		t.Fatal(err)
	}
	skinned := surf.Render(cell)
	back, err := surf.Read("m", skinned, layout)
	if err != nil {
		t.Fatalf("ledger-defined surface failed to read its own render: %v\n%s", err, skinned)
	}
	if !flux.SameBehavior(cell, back, layout) {
		t.Fatalf("ledger-defined surface changed behavior:\n%s", skinned)
	}
}

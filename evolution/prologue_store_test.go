package evolution

import (
	"strings"
	"testing"

	"github.com/mdcfrancis/flow/flux"
)

// The prologue round-trips through the ledger, and InstallPrologue seeds the default
// on first run then makes it live.
func TestPrologueLedgerRoundTrip(t *testing.T) {
	le := newLedger(t)
	if got := LoadPrologue(le); got != nil {
		t.Fatalf("empty store should have no prologue, got %v", got)
	}
	if err := InstallPrologue(le); err != nil {
		t.Fatalf("install: %v", err)
	}
	got := LoadPrologue(le)
	if len(got) != len(flux.DefaultPrologue) {
		t.Fatalf("first install must seed the default (%d), got %d", len(flux.DefaultPrologue), len(got))
	}
}

// The system can REWRITE its own vocabulary from data: saving a prologue with a NEW
// derivation and installing it makes a cell that uses that word compile — with no Go
// change. This is the substrate property.
func TestPrologueIsEvolvableFromData(t *testing.T) {
	le := newLedger(t)
	layout := flux.Layout{"a": {Type: flux.TInt, Offset: 0xB0000}, "out": {Type: flux.TInt, Offset: 0xB0004}}

	// `double` is not a built-in derivation.
	if _, err := flux.Compile("m", `(cell c (reads a) (writes out) (write (out (double a))))`, layout); err == nil {
		t.Fatal("expected `double` to be unknown before it is added to the prologue")
	}

	// Add it as data, install, and now the same cell compiles.
	extended := append(append([]flux.Derivation{}, flux.DefaultPrologue...),
		flux.Derivation{Name: "double", Params: []string{"x"}, Body: "(+ x x)"})
	if err := SavePrologue(le, extended); err != nil {
		t.Fatal(err)
	}
	if err := InstallPrologue(le); err != nil {
		t.Fatalf("install extended: %v", err)
	}
	wat, err := flux.Compile("m", `(cell c (reads a) (writes out) (write (out (double a))))`, layout)
	if err != nil {
		t.Fatalf("with `double` in the ledger-resident prologue, the cell must compile: %v", err)
	}
	if !strings.Contains(wat, "i32.add") {
		t.Fatalf("double should expand to an add:\n%s", wat)
	}

	// Restore the default so other tests see the built-in vocabulary.
	if err := flux.SetPrologue(nil); err != nil {
		t.Fatal(err)
	}
}

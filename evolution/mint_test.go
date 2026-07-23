package evolution

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/mdcfrancis/flow/manifest"
	"github.com/mdcfrancis/flow/storage"
)

// TestExtractNewPrimitive parses the mint section out of a structural response: the refactored
// cell comes first (the primary module), the NEW-PRIMITIVE section after.
func TestExtractNewPrimitive(t *testing.T) {
	resp := "Here is the refactored cell:\n```wat\n(module (func (export \"run-tick\") (param i32 i32) (result i32) (i32.const 1)))\n```\n" +
		"NEW-PRIMITIVE urn:hdm:sys:ring\n```wat\n(module (memory 1) (func (export \"run-tick\") (param i32 i32) (result i32) (i32.const 7)))\n```\n"
	urn, wat, ok := extractNewPrimitive(resp)
	if !ok {
		t.Fatal("expected a mint section")
	}
	if urn != "urn:hdm:sys:ring" {
		t.Fatalf("urn = %q, want urn:hdm:sys:ring", urn)
	}
	if !strings.Contains(wat, "i32.const 7") {
		t.Fatalf("extracted the wrong module (want the primitive after the marker): %q", wat)
	}
	// No marker -> not found.
	if _, _, ok := extractNewPrimitive("```wat\n(module)\n```"); ok {
		t.Fatal("no NEW-PRIMITIVE marker must yield found=false")
	}
	// Non-sys URN -> refused.
	if _, _, ok := extractNewPrimitive("NEW-PRIMITIVE urn:hdm:app:x\n```wat\n(module)\n```"); ok {
		t.Fatal("a non-sys primitive URN must be refused")
	}
}

// TestProvisionAndRetireMint: a fresh primitive is stored (resolvable), and retire removes it.
func TestProvisionAndRetireMint(t *testing.T) {
	le, err := storage.NewLedgerEngine(filepath.Join(t.TempDir(), "hdm.db"))
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	defer le.Close()
	repo := manifest.NewRepository(le)
	o := &Orchestrator{ledger: le, repo: repo}

	const purn = "urn:hdm:sys:minttest"
	good := `(module (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 100))
	  (func (export "run-tick") (param i32 i32) (result i32) (i32.const 5)))`

	if !o.provisionMint(purn, good) {
		t.Fatal("a compiling run-tick primitive must be provisioned")
	}
	if _, err := repo.Load(purn); err != nil {
		t.Fatalf("provisioned primitive must be resolvable: %v", err)
	}
	// Provisioning again is refused (never clobber a live cell).
	if o.provisionMint(purn, good) {
		t.Fatal("provisioning over an existing cell must be refused")
	}
	// Retire removes it.
	o.retireMint(purn)
	if _, err := repo.Load(purn); err == nil {
		t.Fatal("retired primitive must no longer resolve")
	}
	// A non-compiling candidate is refused.
	if o.provisionMint("urn:hdm:sys:bad", "(module (this is broken") {
		t.Fatal("a non-compiling candidate must be refused")
	}
}

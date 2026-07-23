package evolution

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mdcfrancis/flow/compiler"
	"github.com/mdcfrancis/flow/manifest"
	"github.com/mdcfrancis/flow/storage"
)

const sharedImport = `(import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 100))`

func topoLedger(t *testing.T) (*storage.LedgerEngine, *manifest.Repository) {
	t.Helper()
	le, err := storage.NewLedgerEngine(filepath.Join(t.TempDir(), "hdm.db"))
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	t.Cleanup(func() { le.Close() })
	return le, manifest.NewRepository(le)
}

func seed(t *testing.T, repo *manifest.Repository, urn, wat string) {
	t.Helper()
	art, err := compiler.NewCompilerService().CompileGenotype(wat)
	if err != nil || !art.SyntaxPassed {
		t.Fatalf("seed compile %s: %v (%s)", urn, err, art.ErrorContext)
	}
	h, _, err := repo.PutCell(urn, wat, art.Bytecode, manifest.SemanticManifest{}, 0)
	if err != nil {
		t.Fatalf("seed put %s: %v", urn, err)
	}
	if err := repo.SeedRef(urn, h); err != nil {
		t.Fatalf("seed ref %s: %v", urn, err)
	}
}

func TestRunFusion(t *testing.T) {
	le, repo := topoLedger(t)

	// Partner returns 5.
	seed(t, repo, "urn:hdm:cell:partner",
		`(module `+sharedImport+`
		  (func (export "run-tick") (param i32 i32) (result i32) i32.const 5))`)

	// Target dispatches to the partner. The URN lives at offset 262144 (in the
	// inbound region, OUTSIDE the state window) since it is dispatch metadata,
	// not observable state. "urn:hdm:cell:partner" is 20 bytes.
	seed(t, repo, "urn:hdm:cell:target",
		`(module `+sharedImport+`
		  (import "hdm:kernel/cell-dispatch" "invoke-cell" (func $d (param i32 i32 i32 i32) (result i32)))
		  (data (i32.const 262144) "urn:hdm:cell:partner")
		  (func (export "run-tick") (param i32 i32) (result i32)
		    i32.const 262144 i32.const 20 i32.const 0 i32.const 0 call $d))`)

	// The fused candidate inlines the partner (returns 5), no dispatch.
	fused := `(module ` + sharedImport + `
	  (func (export "run-tick") (param i32 i32) (result i32) i32.const 5))`

	orch := NewOrchestrator(le, &fakeReasoner{responses: []string{fused}})
	fr, err := orch.RunFusion(context.Background(), "urn:hdm:cell:target", "urn:hdm:cell:partner")
	if err != nil {
		t.Fatalf("fusion: %v", err)
	}
	if fr.Kind != ExecuteCellularFusion {
		t.Fatalf("kind = %v, want fusion", fr.Kind)
	}
	if !fr.Committed {
		t.Fatalf("fusion not committed: %s", fr.Reason)
	}
	if fr.Verdict == nil || fr.Verdict.CandidateFuel >= fr.Verdict.BaselineFuel {
		t.Fatalf("fused cell should lower fuel (bridge removed): %+v", fr.Verdict)
	}
}

func TestRunFission(t *testing.T) {
	le, repo := topoLedger(t)

	// Target computes 3 directly.
	seed(t, repo, "urn:hdm:cell:tgt",
		`(module `+sharedImport+`
		  (func (export "run-tick") (param i32 i32) (result i32) i32.const 3))`)

	// Model returns CORE then DISPATCHER. The dispatcher delegates to the sister
	// URN "urn:hdm:cell:tgt:sister" (23 bytes @512).
	core := `(module ` + sharedImport + `
	  (func (export "run-tick") (param i32 i32) (result i32) i32.const 3))`
	dispatcher := `(module ` + sharedImport + `
	  (import "hdm:kernel/cell-dispatch" "invoke-cell" (func $d (param i32 i32 i32 i32) (result i32)))
	  (data (i32.const 262144) "urn:hdm:cell:tgt:sister")
	  (func (export "run-tick") (param i32 i32) (result i32)
	    i32.const 262144 i32.const 23 i32.const 0 i32.const 0 call $d))`
	resp := "CORE:\n" + core + "\nDISPATCHER:\n" + dispatcher

	orch := NewOrchestrator(le, &fakeReasoner{responses: []string{resp}})
	fr, err := orch.RunFission(context.Background(), "urn:hdm:cell:tgt")
	if err != nil {
		t.Fatalf("fission: %v", err)
	}
	if fr.Kind != ExecuteCellularFission {
		t.Fatalf("kind = %v, want fission", fr.Kind)
	}
	if !fr.Committed {
		t.Fatalf("fission not committed: %s", fr.Reason)
	}
	// The sister cell must now be live and independently resolvable.
	if _, err := repo.Load("urn:hdm:cell:tgt:sister"); err != nil {
		t.Fatalf("sister cell not seeded: %v", err)
	}
	// The dispatcher (resolving to the sister) reproduced the original's output.
	if fr.Verdict == nil || !fr.Verdict.OutputMatch {
		t.Fatalf("dispatcher did not reproduce original: %+v", fr.Verdict)
	}
}

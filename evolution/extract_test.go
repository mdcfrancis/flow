package evolution

import (
	"context"
	"strings"
	"testing"
)

// delegator returns a run-tick dispatcher that writes componentURN into memory
// and delegates to it via invoke-cell.
func delegator(componentURN string) string {
	return `(module ` + sharedImport + `
	  (import "hdm:kernel/cell-dispatch" "invoke-cell" (func $d (param i32 i32 i32 i32) (result i32)))
	  (data (i32.const 262144) "` + componentURN + `")
	  (func (export "run-tick") (param i32 i32) (result i32)
	    i32.const 262144 i32.const ` + itoa(len(componentURN)) + ` i32.const 0 i32.const 0 call $d))`
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func TestRunExtractionSharesComponent(t *testing.T) {
	le, repo := topoLedger(t)
	const shURN = "urn:hdm:cell:shared"
	// Two cells with the same behavior (both return 3) — a shared component.
	seed(t, repo, "urn:hdm:cell:a", `(module `+sharedImport+`
	  (func (export "run-tick") (param i32 i32) (result i32) i32.const 3))`)
	seed(t, repo, "urn:hdm:cell:b", `(module `+sharedImport+`
	  (func (export "run-tick") (param i32 i32) (result i32) i32.const 3))`)

	shared := `(module ` + sharedImport + `
	  (func (export "run-tick") (param i32 i32) (result i32) i32.const 3))`
	resp := "SHARED:\n" + shared + "\nCELL_A:\n" + delegator(shURN) + "\nCELL_B:\n" + delegator(shURN)

	orch := NewOrchestrator(le, &fakeReasoner{responses: []string{resp}})
	fr, err := orch.RunExtraction(context.Background(), "urn:hdm:cell:a", "urn:hdm:cell:b", shURN)
	if err != nil {
		t.Fatalf("extraction: %v", err)
	}
	if !fr.Committed {
		t.Fatalf("extraction not committed: %s", fr.Reason)
	}
	// Shared component is live and both cells now delegate to it.
	if _, err := repo.Load(shURN); err != nil {
		t.Fatalf("shared component not seeded: %v", err)
	}
	for _, u := range []string{"urn:hdm:cell:a", "urn:hdm:cell:b"} {
		d, _ := repo.Load(u)
		g, _ := repo.Genotype(d)
		if !strings.Contains(g, "invoke-cell") {
			t.Fatalf("%s was not rewritten to delegate: %s", u, g)
		}
	}
}

func TestRunExtractionAbortsIfBehaviorDiverges(t *testing.T) {
	le, repo := topoLedger(t)
	const shURN = "urn:hdm:cell:shared2"
	seed(t, repo, "urn:hdm:cell:c", `(module `+sharedImport+`
	  (func (export "run-tick") (param i32 i32) (result i32) i32.const 3))`)
	seed(t, repo, "urn:hdm:cell:d", `(module `+sharedImport+`
	  (func (export "run-tick") (param i32 i32) (result i32) i32.const 3))`)

	// Shared component returns 9 — the delegating cells would NOT reproduce 3.
	shared := `(module ` + sharedImport + `
	  (func (export "run-tick") (param i32 i32) (result i32) i32.const 9))`
	resp := "SHARED:\n" + shared + "\nCELL_A:\n" + delegator(shURN) + "\nCELL_B:\n" + delegator(shURN)

	orch := NewOrchestrator(le, &fakeReasoner{responses: []string{resp}})
	fr, err := orch.RunExtraction(context.Background(), "urn:hdm:cell:c", "urn:hdm:cell:d", shURN)
	if err != nil {
		t.Fatalf("extraction: %v", err)
	}
	if fr.Committed {
		t.Fatal("extraction must abort when behavior diverges")
	}
	// Nothing changed: the shared component was not seeded.
	if _, err := repo.Load(shURN); err == nil {
		t.Fatal("shared component should not persist on a failed extraction")
	}
}

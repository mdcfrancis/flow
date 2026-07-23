package evolution

import (
	"context"
	"testing"

	"github.com/mdcfrancis/flow/codependency"
	"github.com/mdcfrancis/flow/compiler"
	"github.com/mdcfrancis/flow/manifest"
	"github.com/mdcfrancis/flow/tapes"
)

// TestCoMutationRecordsJointFailure proves that when a committed change to X
// regresses a dependent peer Y (which dispatches to X), the co-mutation tracker
// tracker records the joint failure.
func TestCoMutationRecordsJointFailure(t *testing.T) {
	ctx := context.Background()
	le, repo := topoLedger(t)
	cs := compiler.NewCompilerService()

	const xURN, yURN = "urn:hdm:x", "urn:hdm:y"

	// X v1 returns 5.
	seed(t, repo, xURN, `(module `+sharedImport+`
	  (func (export "run-tick") (param i32 i32) (result i32) i32.const 5))`)

	// Y dispatches to X ("urn:hdm:x" is 9 bytes @262144) and returns its result.
	seed(t, repo, yURN, `(module `+sharedImport+`
	  (import "hdm:kernel/cell-dispatch" "invoke-cell" (func $d (param i32 i32 i32 i32) (result i32)))
	  (data (i32.const 262144) "urn:hdm:x")
	  (func (export "run-tick") (param i32 i32) (result i32)
	    i32.const 262144 i32.const 9 i32.const 0 i32.const 0 call $d))`)

	orch := NewOrchestrator(le, nil)
	orch.Gravity = codependency.NewTracker(le)
	orch.Tapes = NewTapeStore(le)

	// Record Y's tapes while X still returns 5 (Y observes 5).
	yDesc, _ := repo.Load(yURN)
	yPheno, _ := repo.Phenotype(yDesc)
	cases, _, err := orch.compactCorpus(ctx, yPheno)
	if err != nil {
		t.Fatalf("record Y corpus: %v", err)
	}
	frames := make([]*tapes.TransactionFrame, len(cases))
	for i, c := range cases {
		frames[i] = c.Frame
	}
	if err := orch.Tapes.Append(yURN, frames); err != nil {
		t.Fatalf("append Y tapes: %v", err)
	}

	// A prior optimization attempt on X gives gravity a denominator.
	orch.Gravity.RecordIsolationAttempt(xURN)

	// X "evolves" to v2 returning 9 (a committed change Y did not anticipate).
	xv2, _ := cs.CompileGenotype(`(module ` + sharedImport + `
	  (func (export "run-tick") (param i32 i32) (result i32) i32.const 9))`)
	xh, _, _ := repo.PutCell(xURN, "(module)", xv2.Bytecode, manifest.SemanticManifest{}, 0)
	repo.SeedRef(xURN, xh)

	// The co-mutation check must observe Y regressing (now sees 9, recorded 5).
	orch.checkCoMutation(ctx, xURN, []string{yURN})

	m, _ := orch.Gravity.Load()
	if m.Joint[xURN][yURN] < 1 {
		t.Fatalf("expected a joint failure X->Y, got %d", m.Joint[xURN][yURN])
	}
	if g := m.Gravity(xURN, yURN); g <= 0 {
		t.Fatalf("gravity(X,Y) = %v, want > 0", g)
	}
}

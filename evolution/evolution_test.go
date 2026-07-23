package evolution

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mdcfrancis/flow/compiler"
	"github.com/mdcfrancis/flow/manifest"
	"github.com/mdcfrancis/flow/storage"
	"github.com/mdcfrancis/flow/tapes"
)

// fakeReasoner returns canned completions in order, repeating the last one.
type fakeReasoner struct {
	responses []string
	calls     int
	err       error
}

func (f *fakeReasoner) InvokeReasoning(ctx context.Context, sys, user string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	i := f.calls
	if i >= len(f.responses) {
		i = len(f.responses) - 1
	}
	f.calls++
	return f.responses[i], nil
}

// Candidate: memory-bearing, reads the inbound payload byte into the state
// region and returns 1. Leaner than the baseline (no helper call).
const validCell = `(module
  (memory (export "mem") 2)
  (func (export "run-tick") (param $p i32) (param $len i32) (result i32)
    i32.const 0 local.get $p i32.load8_u i32.store8
    i32.const 1))`

// Baseline: same observable behavior as validCell but burns extra fuel via a
// redundant helper call.
const baselineCell = `(module
  (memory (export "mem") 2)
  (func $noop)
  (func (export "run-tick") (param $p i32) (param $len i32) (result i32)
    call $noop
    i32.const 0 local.get $p i32.load8_u i32.store8
    i32.const 1))`

func TestExtractWAT(t *testing.T) {
	fenced := "Here is the optimized cell:\n```wat\n" + validCell + "\n```\nDone."
	if got := extractWAT(fenced); !strings.HasPrefix(got, "(module") || !strings.HasSuffix(got, ")") {
		t.Errorf("fenced extraction failed: %q", got)
	}
	prose := "I think this works: " + validCell + " -- let me know."
	got := extractWAT(prose)
	if !strings.HasPrefix(got, "(module") || strings.Contains(got, "let me know") {
		t.Errorf("prose extraction failed: %q", got)
	}
}

func TestRunSieveConverges(t *testing.T) {
	broken := `(module (func (export "run-tick") (param i32 i32) (result i32) i32.const 1` // missing closes
	model := &fakeReasoner{responses: []string{broken, validCell}}
	out, err := RunSieve(context.Background(), model, "compass", "seed", 5)
	if err != nil {
		t.Fatalf("expected convergence, got %v", err)
	}
	if out.Iterations != 2 {
		t.Errorf("converged at iteration %d, want 2", out.Iterations)
	}
	if !out.Artifact.SyntaxPassed {
		t.Error("artifact not marked SyntaxPassed")
	}
}

func TestRunSieveNonConvergence(t *testing.T) {
	broken := `(module (func (export "x")` // never valid
	model := &fakeReasoner{responses: []string{broken}}
	_, err := RunSieve(context.Background(), model, "compass", "seed", 3)
	if err == nil {
		t.Fatal("expected non-convergence error")
	}
	if model.calls != 3 {
		t.Errorf("model invoked %d times, want 3 (maxIters)", model.calls)
	}
}

func TestRunSievePropagatesReasonerError(t *testing.T) {
	model := &fakeReasoner{err: errors.New("cognitive engine offline")}
	if _, err := RunSieve(context.Background(), model, "c", "s", 5); err == nil {
		t.Fatal("expected reasoner error to propagate")
	}
}

func TestGauntletAcceptsLeanerCandidate(t *testing.T) {
	cs := compiler.NewCompilerService()
	base, _ := cs.CompileGenotype(baselineCell)
	cand, _ := cs.CompileGenotype(validCell)
	v, err := RunGauntlet(context.Background(), base.Bytecode, cand.Bytecode, "run-tick", []uint64{0, 0}, 0, 0, 0)
	if err != nil {
		t.Fatalf("gauntlet: %v", err)
	}
	if !v.OutputMatch {
		t.Fatalf("outputs should match (both return 1)")
	}
	if v.CandidateFuel >= v.BaselineFuel {
		t.Fatalf("candidate fuel %d not less than baseline %d", v.CandidateFuel, v.BaselineFuel)
	}
	if !v.Accepted {
		t.Fatalf("leaner, behavior-preserving candidate rejected: %s", v.Reason)
	}
}

func TestGauntletRejectsDivergence(t *testing.T) {
	cs := compiler.NewCompilerService()
	base, _ := cs.CompileGenotype(baselineCell)
	diverge, _ := cs.CompileGenotype(`(module (func (export "run-tick") (param i32 i32) (result i32) i32.const 2))`)
	v, err := RunGauntlet(context.Background(), base.Bytecode, diverge.Bytecode, "run-tick", []uint64{0, 0}, 0, 0, 0)
	if err != nil {
		t.Fatalf("gauntlet: %v", err)
	}
	if v.Accepted {
		t.Fatal("candidate with divergent output must be rejected")
	}
	if v.OutputMatch {
		t.Fatal("outputs differ (1 vs 2) but reported as matching")
	}
}

func TestRunGauntletTapes(t *testing.T) {
	ctx := context.Background()
	cs := compiler.NewCompilerService()
	base, _ := cs.CompileGenotype(baselineCell)
	cand, _ := cs.CompileGenotype(validCell)
	diverge, _ := cs.CompileGenotype(`(module
	  (memory (export "mem") 2)
	  (func (export "run-tick") (param $p i32) (param $len i32) (result i32)
	    i32.const 0 local.get $p i32.load8_u i32.store8
	    i32.const 2))`)

	// Record a small corpus from the baseline over varied inputs.
	var corpus []*tapes.TransactionFrame
	for i := 0; i < 5; i++ {
		payload := append([]byte{byte(i * 40)}, []byte("-txn")...)
		var seed [16]byte
		tape, err := RecordTape(ctx, base.Bytecode, "run-tick", DefaultPayloadOffset, DefaultStateWindow, payload, uint64(i+1), seed)
		if err != nil {
			t.Fatalf("record tape %d: %v", i, err)
		}
		corpus = append(corpus, tape)
	}

	// Behavior-preserving, leaner candidate must be accepted across all tapes.
	v, err := RunGauntletTapes(ctx, base.Bytecode, cand.Bytecode, "run-tick", corpus, DefaultPayloadOffset, DefaultStateWindow, 0, 0, 0)
	if err != nil {
		t.Fatalf("tape gauntlet: %v", err)
	}
	if !v.Accepted {
		t.Fatalf("leaner candidate rejected: %s (matched %d/%d)", v.Reason, v.TapesMatched, v.TapesRun)
	}
	if v.TapesMatched != len(corpus) {
		t.Fatalf("matched %d tapes, want %d", v.TapesMatched, len(corpus))
	}
	if v.CandidateFuel >= v.BaselineFuel {
		t.Fatalf("candidate fuel %d not below baseline %d", v.CandidateFuel, v.BaselineFuel)
	}

	// A candidate that diverges on the recorded output must be rejected.
	vd, err := RunGauntletTapes(ctx, base.Bytecode, diverge.Bytecode, "run-tick", corpus, DefaultPayloadOffset, DefaultStateWindow, 0, 0, 0)
	if err != nil {
		t.Fatalf("tape gauntlet (diverge): %v", err)
	}
	if vd.Accepted {
		t.Fatal("divergent candidate accepted by tape gauntlet")
	}
}

// TestDeterministicReplay proves the seeded chronos envelope makes a
// clock-reading cell reproducible: an identical candidate must be accepted even
// though run-tick stores the current time into its state region — because the
// tape rebinds now-ns to a fixed value on every replay.
func TestDeterministicReplay(t *testing.T) {
	ctx := context.Background()
	cs := compiler.NewCompilerService()
	// Reads the low 32 bits of now-ns into the state window.
	clockCell := `(module
	  (memory (export "mem") 2)
	  (import "hdm:kernel/chronos" "now-ns" (func $now (result i64)))
	  (func (export "run-tick") (param i32 i32) (result i32)
	    i32.const 0 call $now i32.wrap_i64 i32.store
	    i32.const 1))`
	art, err := cs.CompileGenotype(clockCell)
	if err != nil || !art.SyntaxPassed {
		t.Fatalf("compile clock cell: %v", err)
	}

	var corpus []*tapes.TransactionFrame
	for i := 0; i < 3; i++ {
		var seed [16]byte
		tape, err := RecordTape(ctx, art.Bytecode, "run-tick", DefaultPayloadOffset, DefaultStateWindow,
			[]byte{byte(i)}, uint64(1000+i), seed)
		if err != nil {
			t.Fatalf("record clock tape: %v", err)
		}
		corpus = append(corpus, tape)
	}

	// An identical candidate must REPRODUCE every tape despite reading the
	// clock (determinism). It is not "accepted" — identical fuel means zero
	// energy drop — but reproducing all tapes proves the clock is deterministic
	// under replay; non-determinism would corrupt the state hash and fail here.
	v, err := RunGauntletTapes(ctx, art.Bytecode, art.Bytecode, "run-tick", corpus, DefaultPayloadOffset, DefaultStateWindow, 0, 0, 0)
	if err != nil {
		t.Fatalf("gauntlet: %v", err)
	}
	if !v.OutputMatch || v.TapesMatched != len(corpus) {
		t.Fatalf("clock cell did not reproduce deterministically: %s (matched %d/%d)", v.Reason, v.TapesMatched, v.TapesRun)
	}
}

func TestOrchestratorRunFrameCommits(t *testing.T) {
	le, err := storage.NewLedgerEngine(filepath.Join(t.TempDir(), "hdm.db"))
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	defer le.Close()

	// Seed the baseline optimizer as a node descriptor (genotype + phenotype).
	cs := compiler.NewCompilerService()
	base, _ := cs.CompileGenotype(baselineCell)
	repo := manifest.NewRepository(le)
	const urn = "urn:hdm:sys:optimizer"
	baseDescHash, baseDesc, err := repo.PutCell(urn, baselineCell, base.Bytecode, manifest.SemanticManifest{}, 0)
	if err != nil {
		t.Fatalf("seed descriptor: %v", err)
	}
	if err := repo.SeedRef(urn, baseDescHash); err != nil {
		t.Fatalf("seed ref: %v", err)
	}

	// The cognitive engine returns a leaner, behavior-preserving candidate.
	model := &fakeReasoner{responses: []string{"```wat\n" + validCell + "\n```"}}
	orch := NewOrchestrator(le, model)

	fr, err := orch.RunFrame(context.Background(), urn)
	if err != nil {
		t.Fatalf("run frame: %v", err)
	}
	if !fr.Attempted {
		t.Fatalf("frame not attempted: %s", fr.Reason)
	}
	if !fr.Committed {
		t.Fatalf("expected commit, got: %s", fr.Reason)
	}
	// Reference must have advanced to a new descriptor whose genotype and
	// phenotype both differ from the baseline.
	newDescHash, _ := le.GetRef(urn)
	if newDescHash == baseDescHash {
		t.Fatal("optimizer reference did not advance after commit")
	}
	newDesc, err := repo.Load(urn)
	if err != nil {
		t.Fatalf("load evolved descriptor: %v", err)
	}
	if newDesc.PhenotypeHash == baseDesc.PhenotypeHash {
		t.Fatal("phenotype hash did not change after evolution")
	}
	if newDesc.GenotypeHash == baseDesc.GenotypeHash {
		t.Fatal("genotype hash did not change after evolution")
	}
	if fr.NewRoot == "" {
		t.Fatal("commit did not report a new manifest root")
	}
}

// TestEffectfulGuardRejectsDroppedCall proves that a candidate which removes an
// effectful host call is rejected even though it reproduces every tape — the
// structural invariant, not behavioral equivalence, protects side effects.
func TestEffectfulGuardRejectsDroppedCall(t *testing.T) {
	le, err := storage.NewLedgerEngine(filepath.Join(t.TempDir(), "hdm.db"))
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	defer le.Close()

	// Baseline calls an effectful host import whose result it drops, so the
	// call has no observable sandbox effect — yet it must be preserved.
	baseWAT := `(module
	  (memory (export "mem") 2)
	  (import "hdm:kernel/cognitive-engine" "invoke-reasoning"
	    (func $reason (param i32 i32 i32 i32) (result i32 i32)))
	  (func (export "run-tick") (param $p i32) (param $len i32) (result i32)
	    i32.const 0 i32.const 0 i32.const 0 i32.const 0
	    call $reason drop drop
	    i32.const 1))`
	// Candidate drops the reasoning call entirely (behaviorally identical in the
	// sandbox: still returns 1, touches no memory).
	candWAT := `(module
	  (memory (export "mem") 2)
	  (import "hdm:kernel/cognitive-engine" "invoke-reasoning"
	    (func $reason (param i32 i32 i32 i32) (result i32 i32)))
	  (func (export "run-tick") (param $p i32) (param $len i32) (result i32)
	    i32.const 1))`

	cs := compiler.NewCompilerService()
	baseArt, _ := cs.CompileGenotype(baseWAT)
	const urn = "urn:hdm:sys:optimizer"
	repo := manifest.NewRepository(le)
	sem := manifest.SemanticManifest{EffectfulImports: []manifest.ImportRef{
		{Module: "hdm:kernel/cognitive-engine", Name: "invoke-reasoning"},
	}}
	descHash, _, err := repo.PutCell(urn, baseWAT, baseArt.Bytecode, sem, 0)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	repo.SeedRef(urn, descHash)

	model := &fakeReasoner{responses: []string{candWAT}}
	orch := NewOrchestrator(le, model)
	fr, err := orch.RunFrame(context.Background(), urn)
	if err != nil {
		t.Fatalf("frame: %v", err)
	}
	if fr.Committed {
		t.Fatal("candidate dropping the effectful call must not be committed")
	}
	if !strings.Contains(fr.Reason, "effectful call") {
		t.Fatalf("expected effectful-call rejection, got: %s", fr.Reason)
	}
}

func TestSelectTargetFriction(t *testing.T) {
	le, err := storage.NewLedgerEngine(filepath.Join(t.TempDir(), "hdm.db"))
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	defer le.Close()
	repo := manifest.NewRepository(le)
	cs := compiler.NewCompilerService()

	// Two cells: the baseline burns more fuel (higher friction) than the lean one.
	heavy, _ := cs.CompileGenotype(baselineCell)
	lean, _ := cs.CompileGenotype(validCell)
	hd, _, _ := repo.PutCell("urn:hdm:cell:heavy", baselineCell, heavy.Bytecode, manifest.SemanticManifest{}, 0)
	repo.SeedRef("urn:hdm:cell:heavy", hd)
	ld, _, _ := repo.PutCell("urn:hdm:cell:lean", validCell, lean.Bytecode, manifest.SemanticManifest{}, 0)
	repo.SeedRef("urn:hdm:cell:lean", ld)

	target, err := SelectTarget(context.Background(), repo,
		[]string{"urn:hdm:cell:lean", "urn:hdm:cell:heavy"}, "run-tick", []uint64{0, 0}, 0)
	if err != nil {
		t.Fatalf("select target: %v", err)
	}
	if target != "urn:hdm:cell:heavy" {
		t.Fatalf("friction discovery picked %q, want the heavier cell", target)
	}
}

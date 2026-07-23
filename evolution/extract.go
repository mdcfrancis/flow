package evolution

// Shared-component extraction: given two cells that share logic, synthesize a
// common component and rewrite BOTH cells to delegate to it. It is fission
// generalized to two cells — one extracted CORE, shared by two DISPATCHERS.
// Accepted on behavior preservation alone (extraction trades inline fuel for a
// shared component), and only when BOTH rewrites reproduce their originals; if
// either does not, nothing changes.

import (
	"context"
	"fmt"

	"github.com/mdcfrancis/flow/compiler"
	"github.com/mdcfrancis/flow/manifest"
)

// RunExtraction collapses the shared logic of cells aURN and bURN into a new
// common component at componentURN that both delegate to. Behavior-preserving
// and gauntlet-gated. Returns a committed FrameResult on success.
func (o *Orchestrator) RunExtraction(ctx context.Context, aURN, bURN, componentURN string) (*FrameResult, error) {
	fr := &FrameResult{TargetURN: aURN, Kind: ExecuteCellularFusion}

	baseRoot, err := o.mvcc.InitManifest()
	if err != nil {
		return nil, fmt.Errorf("manifest anchor: %w", err)
	}
	aDesc, err := o.repo.Load(aURN)
	if err != nil {
		return nil, fmt.Errorf("load %s: %w", aURN, err)
	}
	bDesc, err := o.repo.Load(bURN)
	if err != nil {
		return nil, fmt.Errorf("load %s: %w", bURN, err)
	}
	aGeno, err := o.repo.Genotype(aDesc)
	if err != nil {
		return nil, fmt.Errorf("genotype %s: %w", aURN, err)
	}
	bGeno, err := o.repo.Genotype(bDesc)
	if err != nil {
		return nil, fmt.Errorf("genotype %s: %w", bURN, err)
	}
	aBase, err := o.repo.Phenotype(aDesc)
	if err != nil {
		return nil, fmt.Errorf("phenotype %s: %w", aURN, err)
	}
	bBase, err := o.repo.Phenotype(bDesc)
	if err != nil {
		return nil, fmt.Errorf("phenotype %s: %w", bURN, err)
	}

	seed := fmt.Sprintf(`Two cells share logic. Extract the COMMON routine into a shared component and
rewrite BOTH cells to delegate to it. Output THREE complete WAT (module ...) forms
IN ORDER, nothing else:
1. SHARED: exports run-tick, contains the common routine. Its URN is %q.
2. CELL_A: exports run-tick, imports hdm:kernel/cell-dispatch "invoke-cell",
   writes %q into memory and calls invoke-cell to delegate the shared work,
   preserving the FIRST cell's observable behavior EXACTLY.
3. CELL_B: same, preserving the SECOND cell's observable behavior EXACTLY.

FIRST CELL (%s):
%s

SECOND CELL (%s):
%s`, componentURN, componentURN, aURN, aGeno, bURN, bGeno)

	resp, rerr := o.model.InvokeReasoning(ctx, o.compass(), seed)
	if rerr != nil {
		fr.Reason = "extraction synthesis skipped: " + rerr.Error()
		return fr, nil
	}
	mods := extractAllWAT(resp)
	if len(mods) < 3 {
		fr.Reason = fmt.Sprintf("extraction needs 3 modules, model emitted %d", len(mods))
		return fr, nil
	}
	cs := compiler.NewCompilerService()
	shArt, e0 := cs.CompileGenotype(mods[0])
	aArt, e1 := cs.CompileGenotype(mods[1])
	bArt, e2 := cs.CompileGenotype(mods[2])
	if e0 != nil || shArt == nil || !shArt.SyntaxPassed {
		fr.Reason = fmt.Sprintf("shared component failed to compile: %v", e0)
		return fr, nil
	}
	if e1 != nil || aArt == nil || !aArt.SyntaxPassed {
		fr.Reason = fmt.Sprintf("rewritten %s failed to compile: %v", aURN, e1)
		return fr, nil
	}
	if e2 != nil || bArt == nil || !bArt.SyntaxPassed {
		fr.Reason = fmt.Sprintf("rewritten %s failed to compile: %v", bURN, e2)
		return fr, nil
	}
	fr.Attempted = true

	// Resolve the not-yet-live component to its freshly compiled bytecode so the
	// dispatchers can be verified against it.
	resolver := func(urn string) ([]byte, bool) {
		if urn == componentURN {
			return shArt.Bytecode, true
		}
		return o.resolver()(urn)
	}

	// Both rewrites must reproduce their originals before anything is committed.
	if ok, reason := o.reproduces(ctx, aBase, aArt.Bytecode, resolver); !ok {
		fr.Reason = fmt.Sprintf("extraction: %s did not preserve behavior: %s", aURN, reason)
		return fr, nil
	}
	if ok, reason := o.reproduces(ctx, bBase, bArt.Bytecode, resolver); !ok {
		fr.Reason = fmt.Sprintf("extraction: %s did not preserve behavior: %s", bURN, reason)
		return fr, nil
	}

	// Commit the shared component, then hot-swap A and B to their delegating forms.
	shSem := manifest.SemanticManifest{
		FunctionalIntent: fmt.Sprintf("shared component extracted from %s and %s", aURN, bURN),
		DomainTags:       []string{"shared", "extracted"},
	}
	shHash, _, err := o.repo.PutCell(componentURN, mods[0], shArt.Bytecode, shSem, 0)
	if err != nil {
		return nil, fmt.Errorf("persist shared component: %w", err)
	}
	if err := o.repo.SeedRef(componentURN, shHash); err != nil {
		return nil, fmt.Errorf("point shared component ref: %w", err)
	}
	aHash, _, err := o.repo.PutCell(aURN, mods[1], aArt.Bytecode, aDesc.Semantics, aDesc.Saliency)
	if err != nil {
		return nil, fmt.Errorf("persist %s: %w", aURN, err)
	}
	root2, cmErr := o.mvcc.ProposeEvolutionCommit(ctx, baseRoot, aURN, aHash)
	if cmErr != nil {
		fr.Reason = fmt.Sprintf("extraction commit aborted for %s: %v", aURN, cmErr)
		return fr, nil
	}
	bHash, _, err := o.repo.PutCell(bURN, mods[2], bArt.Bytecode, bDesc.Semantics, bDesc.Saliency)
	if err != nil {
		return nil, fmt.Errorf("persist %s: %w", bURN, err)
	}
	root3, cmErr := o.mvcc.ProposeEvolutionCommit(ctx, root2, bURN, bHash)
	if cmErr != nil {
		fr.Reason = fmt.Sprintf("extraction commit aborted for %s: %v", bURN, cmErr)
		return fr, nil
	}

	fr.Committed = true
	fr.NewRoot = root3
	fr.Reason = fmt.Sprintf("extracted shared %s from %s and %s", componentURN, aURN, bURN)
	o.event("fusion", componentURN, fmt.Sprintf("shared component extracted from %s + %s", aURN, bURN))
	return fr, nil
}

// reproduces reports whether candidate reproduces baseline's behavior across a
// compacted regression corpus, accepting on behavior preservation alone (energy
// ignored — extraction trades fuel for sharing).
func (o *Orchestrator) reproduces(ctx context.Context, baseline, candidate []byte, resolver CellResolver) (bool, string) {
	cases, _, cerr := o.compactCorpus(ctx, baseline)
	if cerr != nil {
		return false, "corpus: " + cerr.Error()
	}
	v, verr := RunGauntletCases(ctx, baseline, candidate, EntryPoint, cases,
		o.PayloadOffset, o.StateWindow, o.TokenMilliCents, o.Saliency, behaviorOnlyDrop, resolver)
	if verr != nil {
		return false, "gauntlet: " + verr.Error()
	}
	if !v.Accepted {
		return false, v.Reason
	}
	return true, ""
}

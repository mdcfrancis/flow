package evolution

import (
	"context"
	"fmt"

	"github.com/mdcfrancis/flow/compiler"
)

// behaviorOnlyDrop lets a candidate be accepted purely on behavior preservation,
// ignoring energy — used for Fission, which trades fuel for modularity/size.
const behaviorOnlyDrop = -1e18

// RunTopologyFrame decides the structural mutation for target (given its peers)
// and dispatches to the matching pass: fusion, fission, or ordinary genotype
// refinement.
func (o *Orchestrator) RunTopologyFrame(ctx context.Context, target string, peers []string) (*FrameResult, error) {
	desc, err := o.repo.Load(target)
	if err != nil {
		return nil, fmt.Errorf("load descriptor: %w", err)
	}
	baseline, err := o.repo.Phenotype(desc)
	if err != nil {
		return nil, fmt.Errorf("load phenotype: %w", err)
	}
	plan, err := PlanTopology(target, desc, len(baseline), o.Gravity, peers,
		o.lastLatency[target], o.GravityThreshold, o.LatencyThresholdNS)
	if err != nil {
		return nil, err
	}
	var fr *FrameResult
	switch plan.Kind {
	case ExecuteCellularFusion:
		o.phase("fusing", "merging "+target+" with "+plan.Partner, target)
		fr, err = o.RunFusion(ctx, target, plan.Partner)
	case ExecuteCellularFission:
		o.phase("splitting", "dividing "+target+" into core + dispatcher", target)
		fr, err = o.RunFission(ctx, target)
	default:
		fr, err = o.RunFrame(ctx, target)
	}
	// Co-mutation tracking: if the committed change regressed a dependent
	// peer, record the joint failure so tightly-coupled cells accrue gravity.
	if err == nil && fr != nil && fr.Committed {
		o.checkCoMutation(ctx, target, peers)
	}
	return fr, err
}

// checkCoMutation re-verifies each peer's recorded tapes against its current
// phenotype (with the committed cell now live via the resolver). A peer that no
// longer reproduces its tapes was regressed by the commit — a joint failure.
func (o *Orchestrator) checkCoMutation(ctx context.Context, committed string, peers []string) {
	if o.Gravity == nil || o.Tapes == nil {
		return
	}
	for _, p := range peers {
		if p == committed {
			continue
		}
		frames, err := o.Tapes.Load(p)
		if err != nil || len(frames) == 0 {
			continue
		}
		desc, err := o.repo.Load(p)
		if err != nil {
			continue
		}
		pheno, err := o.repo.Phenotype(desc)
		if err != nil {
			continue
		}
		cases := make([]RegressionCase, len(frames))
		for i, f := range frames {
			cases[i] = RegressionCase{Frame: f}
		}
		v, verr := RunGauntletCases(ctx, pheno, pheno, EntryPoint, cases,
			o.PayloadOffset, o.StateWindow, o.TokenMilliCents, o.Saliency, behaviorOnlyDrop, o.resolver())
		if verr == nil && v != nil && !v.OutputMatch {
			_, _ = o.Gravity.RecordJointFailure(committed, p)
		}
	}
}

// RunFusion collapses a target cell and a tightly-coupled partner it dispatches
// to into a single flattened cell, removing the bridge. The candidate is
// verified against the dispatching baseline (resolver-backed) and accepted only
// if it reproduces behavior with lower energy (the bridge tax removed).
func (o *Orchestrator) RunFusion(ctx context.Context, target, partner string) (*FrameResult, error) {
	fr := &FrameResult{TargetURN: target, Kind: ExecuteCellularFusion}

	baseRoot, err := o.mvcc.InitManifest()
	if err != nil {
		return nil, fmt.Errorf("manifest anchor: %w", err)
	}
	tDesc, err := o.repo.Load(target)
	if err != nil {
		return nil, fmt.Errorf("load target: %w", err)
	}
	tGeno, err := o.repo.Genotype(tDesc)
	if err != nil {
		return nil, fmt.Errorf("load target genotype: %w", err)
	}
	baseline, err := o.repo.Phenotype(tDesc)
	if err != nil {
		return nil, fmt.Errorf("load target phenotype: %w", err)
	}
	pDesc, err := o.repo.Load(partner)
	if err != nil {
		return nil, fmt.Errorf("load partner: %w", err)
	}
	pGeno, err := o.repo.Genotype(pDesc)
	if err != nil {
		return nil, fmt.Errorf("load partner genotype: %w", err)
	}

	seed := fmt.Sprintf(`Fuse cell %s with the partner cell %s it dispatches to.
Inline the partner's logic directly, REMOVE the hdm:kernel/cell-dispatch import
and the invoke-cell call, preserve the exact run-tick behavior, and lower fuel by
eliminating the bridge crossing.

CELL %s:
%s

PARTNER %s:
%s`, target, partner, target, tGeno, partner, pGeno)

	sieve, serr := RunSieve(ctx, o.sieveModel(target), o.compass(), seed, o.SieveMaxIters, RunTickContract)
	if serr != nil {
		fr.Sieve = sieve
		fr.Reason = fmt.Sprintf("fusion synthesis skipped: %v", serr)
		return fr, nil
	}
	fr.Sieve = sieve
	fr.Attempted = true

	// A fused cell must not drop the target's effectful calls.
	for _, eff := range tDesc.Semantics.EffectfulImports {
		baseN, _ := compiler.CountImportCalls(tGeno, eff.Module, eff.Name)
		candN, cErr := compiler.CountImportCalls(sieve.WAT, eff.Module, eff.Name)
		if cErr != nil || candN < baseN {
			fr.Reason = fmt.Sprintf("fusion rejected: drops effectful call %s/%s (%d->%d)", eff.Module, eff.Name, baseN, candN)
			return fr, nil
		}
	}

	// Verify against the DISPATCHING baseline (resolver follows target->partner).
	cases, considered, cerr := o.compactCorpus(ctx, baseline)
	if cerr != nil {
		return nil, fmt.Errorf("compact corpus: %w", cerr)
	}
	fr.CorpusKept, fr.CorpusConsidered = len(cases), considered
	verdict, verr := RunGauntletCases(ctx, baseline, sieve.Artifact.Bytecode, EntryPoint,
		cases, o.PayloadOffset, o.StateWindow, o.TokenMilliCents, o.Saliency, o.MinEnergyDrop, o.resolver())
	if verr != nil {
		fr.Reason = fmt.Sprintf("fusion gauntlet error: %v", verr)
		return fr, nil
	}
	fr.Verdict = verdict
	if !verdict.Accepted {
		fr.Reason = "fusion rejected: " + verdict.Reason
		return fr, nil
	}
	if ok, reason := o.chaosOK(ctx, sieve.Artifact.Bytecode, cases, o.resolver()); !ok {
		fr.Reason = "fusion chaos rejected: " + reason
		return fr, nil
	}

	newDescHash, _, err := o.repo.PutCell(target, sieve.Genotype(), sieve.Artifact.Bytecode, tDesc.Semantics, tDesc.Saliency)
	if err != nil {
		return nil, fmt.Errorf("persist fused descriptor: %w", err)
	}
	newRoot, cmErr := o.mvcc.ProposeEvolutionCommit(ctx, baseRoot, target, newDescHash)
	if cmErr != nil {
		fr.Reason = fmt.Sprintf("fusion commit aborted: %v", cmErr)
		return fr, nil
	}
	fr.Committed = true
	fr.NewRoot = newRoot
	fr.Reason = fmt.Sprintf("fused %s + %s: fuel %d->%d, H %.4f->%.4f",
		target, partner, verdict.BaselineFuel, verdict.CandidateFuel, verdict.BaselineH, verdict.CandidateH)
	return fr, nil
}

// RunFission splits a bloated target into a smaller CORE sister cell plus a thin
// DISPATCHER that delegates to it — decoupling the routine behind the bridge.
// It is accepted on behavior preservation alone (fission trades fuel for size),
// provided the extracted core clears the size threshold.
func (o *Orchestrator) RunFission(ctx context.Context, target string) (*FrameResult, error) {
	fr := &FrameResult{TargetURN: target, Kind: ExecuteCellularFission}

	baseRoot, err := o.mvcc.InitManifest()
	if err != nil {
		return nil, fmt.Errorf("manifest anchor: %w", err)
	}
	tDesc, err := o.repo.Load(target)
	if err != nil {
		return nil, fmt.Errorf("load target: %w", err)
	}
	tGeno, err := o.repo.Genotype(tDesc)
	if err != nil {
		return nil, fmt.Errorf("load target genotype: %w", err)
	}
	baseline, err := o.repo.Phenotype(tDesc)
	if err != nil {
		return nil, fmt.Errorf("load target phenotype: %w", err)
	}

	sisterURN := target + ":sister"
	seed := fmt.Sprintf(`Split cell %s into two modules. Output the CORE module FIRST,
then the DISPATCHER module second.
- CORE: exports run-tick with the heavy routine extracted from the original.
- DISPATCHER: exports run-tick, imports hdm:kernel/cell-dispatch, writes the URN
  %q into memory and calls invoke-cell to delegate, preserving observable behavior.

ORIGINAL GENOTYPE:
%s`, target, sisterURN, tGeno)

	resp, rerr := o.model.InvokeReasoning(ctx, o.compass(), seed)
	if rerr != nil {
		fr.Reason = fmt.Sprintf("fission synthesis skipped: %v", rerr)
		return fr, nil
	}
	mods := extractAllWAT(resp)
	if len(mods) < 2 {
		fr.Reason = fmt.Sprintf("fission needs two modules, model emitted %d", len(mods))
		return fr, nil
	}
	cs := compiler.NewCompilerService()
	coreArt, e1 := cs.CompileGenotype(mods[0])
	dispArt, e2 := cs.CompileGenotype(mods[1])
	if e1 != nil || coreArt == nil || !coreArt.SyntaxPassed {
		fr.Reason = fmt.Sprintf("fission core failed to compile: %v", e1)
		return fr, nil
	}
	if e2 != nil || dispArt == nil || !dispArt.SyntaxPassed {
		fr.Reason = fmt.Sprintf("fission dispatcher failed to compile: %v", e2)
		return fr, nil
	}
	fr.Attempted = true

	// The extracted core must actually be smaller than the fission threshold.
	if len(coreArt.Bytecode) >= FissionPhenotypeBytes {
		fr.Reason = fmt.Sprintf("fission core still %d bytes (>= %d)", len(coreArt.Bytecode), FissionPhenotypeBytes)
		return fr, nil
	}

	// Verify the dispatcher reproduces the original, resolving the not-yet-live
	// sister to the freshly compiled core bytecode.
	resolver := func(urn string) ([]byte, bool) {
		if urn == sisterURN {
			return coreArt.Bytecode, true
		}
		return o.resolver()(urn)
	}
	cases, considered, cerr := o.compactCorpus(ctx, baseline)
	if cerr != nil {
		return nil, fmt.Errorf("compact corpus: %w", cerr)
	}
	fr.CorpusKept, fr.CorpusConsidered = len(cases), considered
	verdict, verr := RunGauntletCases(ctx, baseline, dispArt.Bytecode, EntryPoint,
		cases, o.PayloadOffset, o.StateWindow, o.TokenMilliCents, o.Saliency, behaviorOnlyDrop, resolver)
	if verr != nil {
		fr.Reason = fmt.Sprintf("fission gauntlet error: %v", verr)
		return fr, nil
	}
	fr.Verdict = verdict
	if !verdict.Accepted {
		fr.Reason = "fission rejected: " + verdict.Reason
		return fr, nil
	}
	if ok, reason := o.chaosOK(ctx, dispArt.Bytecode, cases, resolver); !ok {
		fr.Reason = "fission chaos rejected: " + reason
		return fr, nil
	}

	// Commit the sister cell, then hot-swap the target to the dispatcher.
	sisterSem := tDesc.Semantics
	sisterSem.DomainTags = append([]string{"sister"}, tDesc.Semantics.DomainTags...)
	sisterHash, _, err := o.repo.PutCell(sisterURN, mods[0], coreArt.Bytecode, sisterSem, tDesc.Saliency)
	if err != nil {
		return nil, fmt.Errorf("persist sister: %w", err)
	}
	if err := o.repo.SeedRef(sisterURN, sisterHash); err != nil {
		return nil, fmt.Errorf("point sister ref: %w", err)
	}
	dispDescHash, _, err := o.repo.PutCell(target, mods[1], dispArt.Bytecode, tDesc.Semantics, tDesc.Saliency)
	if err != nil {
		return nil, fmt.Errorf("persist dispatcher descriptor: %w", err)
	}
	newRoot, cmErr := o.mvcc.ProposeEvolutionCommit(ctx, baseRoot, target, dispDescHash)
	if cmErr != nil {
		fr.Reason = fmt.Sprintf("fission commit aborted: %v", cmErr)
		return fr, nil
	}
	fr.Committed = true
	fr.NewRoot = newRoot
	fr.Reason = fmt.Sprintf("fissioned %s -> dispatcher + %s (core %d bytes)", target, sisterURN, len(coreArt.Bytecode))
	return fr, nil
}

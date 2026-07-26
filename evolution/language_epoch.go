package evolution

// LANGUAGE EPOCH: a change to Flux itself is accepted the same way any mutation is —
// verify, then promote or roll back — but its test is the WHOLE STACK
// (docs/language-evolution.md §3). This file is the acceptance gate: snapshot the
// affected cells, rebuild the stale stack under the new language version (via the
// memoized rebuild driver), and PROMOTE iff every re-derived cell is green against
// its unchanged scenarios and aggregate fitness improved; otherwise ROLL BACK,
// atomically, restoring the prior refs.
//
// The live language version is ledger-backed (not the FluxLanguageVersion constant,
// which is only the default): promoting a change bumps the stored version, so every
// subsequent authoring records — and is verified against — the new language.

import "github.com/mdcfrancis/flow/storage"

const languageVersionRef = "urn:hdm:language:flux:version"

// LoadLanguageVersion returns the live Flux language version, defaulting to the
// FluxLanguageVersion constant when none has been promoted yet.
func LoadLanguageVersion(ledger *storage.LedgerEngine) string {
	if ledger == nil {
		return FluxLanguageVersion
	}
	if h, err := ledger.GetRef(languageVersionRef); err == nil {
		if raw, rerr := ledger.ReadBlock(h); rerr == nil && len(raw) > 0 {
			return string(raw)
		}
	}
	return FluxLanguageVersion
}

// SaveLanguageVersion sets the live Flux language version (the promote step of an
// accepted language epoch).
func SaveLanguageVersion(ledger *storage.LedgerEngine, v string) error {
	h, err := ledger.WriteBlock([]byte(v))
	if err != nil {
		return err
	}
	return ledger.UpdateRef(languageVersionRef, h)
}

// LanguageChangeReport summarizes a proposed ΔL and its verdict.
type LanguageChangeReport struct {
	From, To   string
	Rebuilt    int  // cells re-derived under the new language
	Reused     int  // cache hits + early-cutoff memo hits (unchanged / already migrated)
	AllGreen   bool // every re-derived cell passed its unchanged scenarios
	OldFitness float64
	NewFitness float64
	Promoted   bool // accepted (green + fitness improved) → the new version is live
}

// FitnessFunc scores a completed rebuild — the north-star aggregate (syntax-valid
// rate, token cost, convergence, canonicality; docs/self-hosting-flux.md §0.1). The
// caller supplies it so the gate stays independent of how fitness is measured.
type FitnessFunc func([]RebuildOutcome) float64

// ProposeLanguageChange runs a language evolution as an ATOMIC EPOCH
// (docs/language-evolution.md §3):
//
//  1. snapshot the affected (stale) cells' current descriptor refs;
//  2. rebuild the stale stack under `to` via the memoized driver + reauthor;
//  3. PROMOTE iff every re-derived cell is green and aggregate fitness improved —
//     bump the live version, keep the new genomes; else ROLL BACK — restore the
//     snapshot refs, leave the old version live.
//
// reauthor performs the real per-cell re-derivation (build + verify against the
// cell's unchanged scenarios); it is injected so the gate is testable and so the
// orchestrator, not this package, owns synthesis.
func ProposeLanguageChange(ledger *storage.LedgerEngine, to string, oldFitness float64, reauthor ReauthorFunc, fitness FitnessFunc) (LanguageChangeReport, error) {
	from := LoadLanguageVersion(ledger)
	rep := LanguageChangeReport{From: from, To: to, OldFitness: oldFitness}
	if to == "" || to == from {
		return rep, nil // nothing to do
	}
	cur := CurrentInputs{Language: to}
	plan := PlanRebuild(ledger, cur)

	// Snapshot every affected cell's current ref so a rejected epoch reverts exactly
	// the cells the rebuild touched — the atomic rollback (docs/language-evolution.md §7).
	snap := make(map[string]string, len(plan.Stale))
	for _, s := range plan.Stale {
		if h, err := ledger.GetRef(s.URN); err == nil && h != "" {
			snap[s.URN] = h
		}
	}

	outcomes, err := plan.Execute(ledger, cur, reauthor)
	if err != nil {
		restoreRefs(ledger, snap) // best-effort rollback on a mid-rebuild failure
		return rep, err
	}
	for _, o := range outcomes {
		if o.Reused {
			rep.Reused++
		} else {
			rep.Rebuilt++
		}
	}
	rep.AllGreen = AllGreen(outcomes)
	if fitness != nil {
		rep.NewFitness = fitness(outcomes)
	}
	// Acceptance: behavior held (all green) AND the north-star fitness improved.
	rep.Promoted = rep.AllGreen && rep.NewFitness > oldFitness
	if rep.Promoted {
		_ = SaveLanguageVersion(ledger, to)
	} else {
		restoreRefs(ledger, snap)
	}
	return rep, nil
}

// restoreRefs re-points each cell URN at its snapshotted descriptor hash — the
// rollback of a rejected language epoch.
func restoreRefs(ledger *storage.LedgerEngine, snap map[string]string) {
	for urn, h := range snap {
		_ = ledger.UpdateRef(urn, h)
	}
}

package evolution

// REBUILD is the memoized evaluation over the lineage graph (docs/lineage.md §5): a
// change to any input becomes one operation — re-derive the transitive closure of
// what depended on it, and reuse everything whose inputs did not move. This file owns
// only the traversal + memo; the actual re-derivation of a cell (re-authoring it
// against its unchanged scenarios) is an injected callback, so the driver is testable
// without a model and the epoch gate (docs/language-evolution.md §3) wires the real
// work in.
//
// The north-star driver is a LANGUAGE change: bumping FluxLanguageVersion moves the
// `language` edge of every Flux cell, marking the whole Flux stack stale in one
// stroke while WAT cells (and anything else unchanged) stay cache hits.

import (
	"strings"

	"github.com/mdcfrancis/flow/storage"
)

// CurrentInputs describes the DESIRED (current) values of the derivation inputs a
// rebuild targets. For now the language version is the dimension a rebuild keys on.
// A node whose recorded Language differs from Language is STALE; every other node is
// a cache HIT.
type CurrentInputs struct {
	Language string // the current language version (e.g. after a ΔL)
}

// langFamily is the language identity without its version — the part before the
// first "/" ("flux/v1" → "flux", "wat" → "wat"). A version bump moves cells within a
// family; it must never touch a cell written in a DIFFERENT language.
func langFamily(v string) string {
	if i := strings.IndexByte(v, '/'); i >= 0 {
		return v[:i]
	}
	return v
}

// stale reports whether a lineage node must be re-derived under cur — its language
// version moved WITHIN ITS FAMILY. A node in a different language family (e.g. a WAT
// cell during a Flux language change), with no language edge, or when no target
// language is set, is never stale on this dimension.
func (cur CurrentInputs) stale(l Lineage) bool {
	if cur.Language == "" || l.Language == "" {
		return false
	}
	return langFamily(l.Language) == langFamily(cur.Language) && l.Language != cur.Language
}

// migrated returns what a node's derivation inputs BECOME under cur: the language
// edge advanced, every other edge unchanged — behavior/scenarios are
// language-independent (docs/language-evolution.md §2), so only the language moves.
func (cur CurrentInputs) migrated(l Lineage) Lineage {
	m := l
	if cur.Language != "" {
		m.Language = cur.Language
	}
	m.Result = "" // the migrated result is not known until it is re-derived
	return m
}

// RebuildPlan partitions the lineage graph under a set of current inputs.
type RebuildPlan struct {
	Hits  []Lineage // inputs unchanged → reuse the stored genotype (the base case)
	Stale []Lineage // an input moved → must be re-derived
}

// PlanRebuild walks every recorded lineage node and partitions it into cache hits
// (reuse) vs stale (re-derive) under cur. This is the INCREMENTAL step: only the
// closure of the changed input is stale; everything else is a hit. A language change
// that a given cell is invariant to still shows as stale here (its language edge
// moved); the early-cutoff in Execute catches the case where re-derivation would
// reproduce an already-recorded bundle.
func PlanRebuild(ledger *storage.LedgerEngine, cur CurrentInputs) RebuildPlan {
	var plan RebuildPlan
	for _, l := range LoadLineages(ledger) {
		if cur.stale(l) {
			plan.Stale = append(plan.Stale, l)
		} else {
			plan.Hits = append(plan.Hits, l)
		}
	}
	return plan
}

// ReauthorFunc re-derives a stale cell under its migrated inputs and returns the new
// genotype hash, whether it verified GREEN against its (unchanged) scenarios, and a
// fitness score (docs/language-evolution.md §2–3). It is the real orchestrator work;
// the rebuild driver owns only the memoized traversal around it.
type ReauthorFunc func(stale, migrated Lineage) (result string, green bool, fitness float64, err error)

// RebuildOutcome is one cell's rebuild result.
type RebuildOutcome struct {
	URN     string
	Result  string  // new (or reused) genotype hash
	Green   bool    // verified green against unchanged scenarios (true for a reuse — unchanged, already committed)
	Reused  bool    // no re-derivation ran: a cache hit or an early-cutoff memo hit
	Fitness float64 // aggregate fitness of the re-derived cell (0 for a reuse)
}

// Execute runs the plan. Cache hits are reused as-is — the base case that terminates
// the recursion so a self-hosted language does not re-derive itself (docs/lineage.md
// §1). Each stale cell is either served from the authoring memo (a prior derivation
// under the exact migrated inputs — early cutoff, docs/lineage.md §5) or re-derived
// via reauthor, whose new lineage is then recorded.
func (plan RebuildPlan) Execute(ledger *storage.LedgerEngine, cur CurrentInputs, reauthor ReauthorFunc) ([]RebuildOutcome, error) {
	outcomes := make([]RebuildOutcome, 0, len(plan.Hits)+len(plan.Stale))
	for _, h := range plan.Hits {
		outcomes = append(outcomes, RebuildOutcome{URN: h.URN, Result: h.Result, Green: true, Reused: true})
	}
	for _, s := range plan.Stale {
		migrated := cur.migrated(s)
		// Early cutoff: if these exact migrated inputs already produced a genotype
		// (e.g. a prior epoch migrated this cell), reuse it — do not re-author.
		if hit, ok := FindLineageByInputs(ledger, migrated.InputsHash()); ok {
			outcomes = append(outcomes, RebuildOutcome{URN: s.URN, Result: hit.Result, Green: true, Reused: true})
			continue
		}
		result, green, fitness, err := reauthor(s, migrated)
		if err != nil {
			return outcomes, err
		}
		migrated.Result = result
		// Only a GREEN re-derivation is memoized, so a later rebuild's early-cutoff
		// never serves a genotype that failed its scenarios (docs/lineage.md §2).
		if green {
			_ = RecordLineage(ledger, migrated)
		}
		outcomes = append(outcomes, RebuildOutcome{URN: s.URN, Result: result, Green: green, Fitness: fitness})
	}
	return outcomes, nil
}

// AllGreen reports whether every RE-DERIVED cell came back green against its
// unchanged scenarios (reused cells are unchanged, so they cannot regress). This is
// the behavioral half of the epoch acceptance gate (docs/language-evolution.md §3);
// the aggregate-fitness half is layered on top by the caller.
func AllGreen(outcomes []RebuildOutcome) bool {
	for _, o := range outcomes {
		if !o.Reused && !o.Green {
			return false
		}
	}
	return true
}

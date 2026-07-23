package appgen

import (
	"context"
	"fmt"
	"strings"

	"github.com/mdcfrancis/flow/evolution"
)

// narrativePrompt asks for a one-line role summary of a component that has just
// been proven correct. It is given the component's role and the checks it
// VERIFIABLY passes, so the prose describes what the cell actually does.
const narrativePrompt = `You summarize ONE component of an application in a single short sentence, for a
map that other engineers read before building neighbouring components.

You are given the component's ROLE and the checks it now VERIFIABLY PASSES.
Describe, in one plain sentence, what it does and which shared state it drives —
concrete and factual, no praise, no speculation about what it might do next.

Output ONLY the sentence, no quotes, no prose around it.`

// RefreshAppMap rebuilds the application's conceptual map from ground truth and
// persists it: for every subsystem it derives what the cell VERIFIABLY reads and
// writes (from its passing coordination checks and a scan of its genotype's
// memory accesses), what it is proven to do, and how far along it is. A component
// that has just converged also gets a one-line model-written summary folded in.
//
// This is the "merge components in as they are built" step: each call re-reads
// reality, so the map an in-flight cell is built against always reflects the
// system as it actually stands.
func (g *Grower) RefreshAppMap(ctx context.Context, namespace string) error {
	env := LoadEnvelope(g.ledger, namespace)
	if env == nil {
		return nil
	}
	contract := evolution.LoadContract(g.ledger, namespace)

	m := evolution.LoadAppMap(g.ledger, namespace)
	if m == nil {
		m = &evolution.AppMap{Namespace: namespace}
	}
	m.Objective = env.Objective

	live := map[string]bool{}
	for _, sub := range env.SubsystemRequirements {
		live[sub.Identity] = true
		comp := evolution.ComponentMap{
			Identity:       sub.Identity,
			Role:           sub.Semantics,
			Entry:          entryFor(sub),
			Status:         "planned",
			DeclaredReads:  sub.Reads,
			DeclaredWrites: sub.Writes,
		}
		if sub.Composition != nil {
			// A generated combinator driver reads its input array and writes its
			// output array — exactly and only. Declare that authoritatively (over the
			// model's free-text ports) so frame-level memoization can soundly skip the
			// driver when its input is unchanged. The driver's own code scan sees only
			// the config region, not the transitively-read input array.
			reads := []string{sub.Composition.In}
			// The driver dispatches the LEAF per element, so it transitively reads
			// whatever the leaf reads (e.g. a max-iterations scalar). Fold those in so
			// the driver's read-set/mask is complete — else an enforced read-mask would
			// hide a field the leaf needs, and memoization could skip a stale result.
			if ld, lerr := g.repo.Load(sub.Composition.Leaf); lerr == nil {
				if lwat, gerr := g.repo.Genotype(ld); gerr == nil {
					lr, _ := evolution.ScanMemoryAccess(lwat, contract)
					reads = append(reads, lr...)
				}
			}
			comp.DeclaredReads = reads
			comp.DeclaredWrites = []string{sub.Composition.Out}
		}

		desc, err := g.repo.Load(sub.Identity)
		if err != nil {
			m.Upsert(comp) // scaffolded but not yet live
			continue
		}
		if fi := strings.TrimSpace(desc.Semantics.FunctionalIntent); fi != "" {
			comp.Role = fi
		}
		wat, _ := g.repo.Genotype(desc)
		bytecode, perr := g.repo.Phenotype(desc)
		suite, _ := evolution.LoadAcceptance(g.ledger, sub.Identity)

		// Facts from the code: which contract fields does it actually touch?
		codeReads, codeWrites := evolution.ScanMemoryAccess(wat, contract)

		// Facts from proof: what do its PASSING checks establish?
		var chkReads, chkWrites, verified []string
		if perr == nil && suite != nil {
			passed := evolution.ScenarioPassFlags(ctx, bytecode, suite.Scenarios,
				evolution.DefaultPayloadOffset, evolution.DefaultStateWindow, nil)
			chkReads, chkWrites, verified = evolution.DeriveFromChecks(suite, passed, contract)
			p, t := evolution.ScoreSuite(ctx, bytecode, comp.Entry, suite,
				evolution.DefaultPayloadOffset, evolution.DefaultStateWindow, nil)
			comp.Passed, comp.Total = p, t
			switch {
			case t > 0 && p >= t:
				comp.Status = "converged"
			case t > 0:
				comp.Status = "building"
			default:
				comp.Status = "live" // no checks to be held to
			}
		} else {
			comp.Status = "live"
		}
		comp.Reads = mergeNames(codeReads, chkReads)
		comp.Writes = mergeNames(codeWrites, chkWrites)
		comp.Verified = verified

		// Fold in a one-line summary the first time a component is proven correct.
		if comp.Status == "converged" && len(verified) > 0 {
			if prev := findComponent(m, sub.Identity); prev == nil || prev.Narrative == "" {
				comp.Narrative = g.narrate(ctx, comp)
			}
		}
		m.Upsert(comp)
	}
	// Drop components no longer in the architecture (e.g. a fractured parent).
	for _, c := range append([]evolution.ComponentMap(nil), m.Components...) {
		if !live[c.Identity] {
			m.Remove(c.Identity)
		}
	}
	return evolution.SaveAppMap(g.ledger, namespace, m)
}

// narrate asks the model for a one-line summary of a just-converged component,
// grounded in the checks it verifiably passes. Best-effort: "" on any fault.
func (g *Grower) narrate(ctx context.Context, c evolution.ComponentMap) string {
	if g.model == nil {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "ROLE: %s\n", c.Role)
	if len(c.Writes) > 0 {
		fmt.Fprintf(&b, "WRITES: %s\n", strings.Join(c.Writes, ", "))
	}
	if len(c.Reads) > 0 {
		fmt.Fprintf(&b, "READS: %s\n", strings.Join(c.Reads, ", "))
	}
	b.WriteString("VERIFIED CHECKS IT PASSES:\n")
	for _, v := range c.Verified {
		fmt.Fprintf(&b, "- %s\n", v)
	}
	resp, err := g.model.InvokeReasoning(ctx, g.prompt("narrative", narrativePrompt), b.String())
	if err != nil {
		return ""
	}
	line := strings.TrimSpace(resp)
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	if len(line) > 200 {
		line = line[:200]
	}
	return strings.Trim(line, `"`)
}

func findComponent(m *evolution.AppMap, id string) *evolution.ComponentMap {
	for i := range m.Components {
		if m.Components[i].Identity == id {
			return &m.Components[i]
		}
	}
	return nil
}

// mergeNames unions two name lists (code-derived and proof-derived facts).
func mergeNames(a, b []string) []string {
	set := map[string]bool{}
	for _, v := range a {
		set[v] = true
	}
	for _, v := range b {
		set[v] = true
	}
	out := make([]string, 0, len(set))
	for _, v := range a {
		if set[v] {
			out = append(out, v)
			delete(set, v)
		}
	}
	for _, v := range b {
		if set[v] {
			out = append(out, v)
			delete(set, v)
		}
	}
	return out
}

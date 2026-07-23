package appgen

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"

	"github.com/mdcfrancis/flow/evolution"
)

// The BOUNDARY is a cell's declared read/write ports over shared state — now hard-enforced
// by the runtime mask. It is a first-class, EVOLVABLE artifact: just as a cell's genotype
// mutates until it passes, its boundary mutates when the declared ports are wrong. The
// signal is the gap between what a cell DECLARES and what its code VERIFIABLY touches, plus
// its purpose and failing checks. Evolving the boundary rewrites the envelope's ports, which
// propagate automatically to the enforced mask, the app map, and the plan.

// BoundaryFriction is the declared-vs-verified gap for a cell. Undeclared accesses mean the
// boundary is too NARROW (or the code is wrong); unused declarations mean it is too WIDE.
type BoundaryFriction struct {
	UndeclaredReads  []string
	UndeclaredWrites []string
	UnusedReads      []string
	UnusedWrites     []string
}

// None reports whether the declared boundary exactly matches verified behavior.
func (f BoundaryFriction) None() bool {
	return len(f.UndeclaredReads)+len(f.UndeclaredWrites)+len(f.UnusedReads)+len(f.UnusedWrites) == 0
}

// boundaryFriction computes the gap between a component's declared ports and what its code
// and passing checks establish it actually reads/writes.
func boundaryFriction(c *evolution.ComponentMap) BoundaryFriction {
	decR, decW := fieldSet(c.DeclaredReads), fieldSet(c.DeclaredWrites)
	verR, verW := fieldSet(c.Reads), fieldSet(c.Writes)
	return BoundaryFriction{
		UndeclaredReads:  minusSet(verR, decR),
		UndeclaredWrites: minusSet(verW, decW),
		UnusedReads:      minusSet(decR, verR),
		UnusedWrites:     minusSet(decW, verW),
	}
}

const boundaryPrompt = `You revise a single cell's SHARED-STATE BOUNDARY — the exact contract
fields it is permitted to read and write. This boundary is HARD-ENFORCED at runtime: the cell
can read ONLY its declared reads (any other field reads as 0) and write ONLY its declared
writes (any other write is discarded). A wrong boundary makes a correct cell impossible.

You are given the cell's PURPOSE, the full shared-state CONTRACT (the only fields that exist),
its CURRENT declared boundary, what its CODE actually touches, any MISMATCH, and — if it is
stuck — its failing checks. Decide the boundary the cell's PURPOSE genuinely requires:
- GRANT a field only if the purpose needs it (deny-by-default; keep the boundary minimal).
- ADD a field the cell provably needs but didn't declare (the mismatch's undeclared accesses,
  when they serve the purpose).
- REMOVE a declared field the cell does not actually use.
- Use the literal "HMI input" for operator input; otherwise use exact snake_case field names
  from the contract. Never invent a field that is not in the contract.

Output ONLY JSON, no prose: {"reads":["field",...],"writes":["field",...]}`

// BoundaryCheck reports whether a cell's boundary is STABLE — a non-empty declared boundary
// with no declared-vs-verified friction, so there is nothing for the model to evolve — and a
// fingerprint of the inputs a boundary decision depends on (its declared + verified ports and
// the objective). A caller can skip stable cells and memoize on the fingerprint to avoid
// re-asking the model about an unchanged situation (bounding token cost on the stall path). A
// composition driver is always "stable" (its ports are forced from in/out).
func (g *Grower) BoundaryCheck(namespace, subIdentity string) (stable bool, fingerprint string) {
	env := LoadEnvelope(g.ledger, namespace)
	var sub *Subsystem
	if env != nil {
		for i := range env.SubsystemRequirements {
			if env.SubsystemRequirements[i].Identity == subIdentity {
				sub = &env.SubsystemRequirements[i]
				break
			}
		}
	}
	if sub == nil {
		return false, ""
	}
	if sub.Composition != nil {
		return true, "composition"
	}
	var comp *evolution.ComponentMap
	if m := evolution.LoadAppMap(g.ledger, namespace); m != nil {
		for i := range m.Components {
			if m.Components[i].Identity == subIdentity {
				comp = &m.Components[i]
				break
			}
		}
	}
	hasBoundary := len(sub.Reads) > 0 || len(sub.Writes) > 0
	if hasBoundary && comp != nil && boundaryFriction(comp).None() {
		stable = true
	}
	var b strings.Builder
	b.WriteString(strings.Join(sortedLower(sub.Reads), ","))
	b.WriteByte('|')
	b.WriteString(strings.Join(sortedLower(sub.Writes), ","))
	if comp != nil {
		b.WriteByte('|')
		b.WriteString(strings.Join(sortedLower(comp.Reads), ","))
		b.WriteByte('|')
		b.WriteString(strings.Join(sortedLower(comp.Writes), ","))
	}
	if env != nil {
		b.WriteByte('|')
		b.WriteString(env.Objective)
	}
	return stable, b.String()
}

// EvolveBoundary reconsiders one cell's boundary as a mutation operator: it asks the model for
// the minimal correct ports given the cell's purpose, the contract, its current boundary, and
// the declared-vs-verified gap, then — if the ports changed — rewrites them in the envelope
// (propagating to the mask, app map, and plan). reason is a short tag ("stalled",
// "objective-change") woven into the prompt. A composition driver's boundary is derived from
// its in/out and is never model-evolved. Returns whether the boundary changed.
func (g *Grower) EvolveBoundary(ctx context.Context, namespace, subIdentity, reason string) (bool, error) {
	env := LoadEnvelope(g.ledger, namespace)
	if env == nil {
		return false, fmt.Errorf("no envelope for %s", namespace)
	}
	idx := -1
	for i := range env.SubsystemRequirements {
		if env.SubsystemRequirements[i].Identity == subIdentity {
			idx = i
			break
		}
	}
	if idx < 0 {
		return false, fmt.Errorf("no subsystem %s in %s", subIdentity, namespace)
	}
	sub := env.SubsystemRequirements[idx]
	if sub.Composition != nil {
		return false, nil // driver ports are forced from in/out — not model-evolved
	}
	contract := evolution.LoadContract(g.ledger, namespace)
	if contract == nil || len(contract.Fields) == 0 {
		return false, nil
	}
	var comp *evolution.ComponentMap
	if m := evolution.LoadAppMap(g.ledger, namespace); m != nil {
		for i := range m.Components {
			if m.Components[i].Identity == subIdentity {
				comp = &m.Components[i]
				break
			}
		}
	}
	resp, err := g.model.InvokeReasoning(ctx, g.prompt("boundary", boundaryPrompt), boundaryPayload(sub, comp, contract, reason))
	if err != nil {
		return false, err
	}
	js := extractJSON(resp)
	if js == "" {
		return false, nil
	}
	var out struct {
		Reads  []string `json:"reads"`
		Writes []string `json:"writes"`
	}
	if json.Unmarshal([]byte(js), &out) != nil {
		return false, nil
	}
	newReads := groundFields(out.Reads, contract)
	newWrites := groundFields(out.Writes, contract)
	// If the model proposed fields that don't exist in the contract, its output was
	// malformed — record it against the boundary prompt for auto-refinement.
	if len(out.Reads) > len(newReads) || len(out.Writes) > len(newWrites) {
		evolution.AddPromptGrievance(g.ledger, "boundary",
			"proposed read/write fields that are NOT in the contract (they were dropped); only real snake_case contract fields or \"HMI input\" are valid")
	}
	if sameFieldSet(newReads, sub.Reads) && sameFieldSet(newWrites, sub.Writes) {
		return false, nil
	}
	env.SubsystemRequirements[idx].Reads = newReads
	env.SubsystemRequirements[idx].Writes = newWrites
	saveEnvelope(g.ledger, env)
	g.event("boundary", subIdentity, fmt.Sprintf("boundary evolved (%s): reads={%s} writes={%s}",
		reason, strings.Join(newReads, ", "), strings.Join(newWrites, ", ")))
	log.Printf("[BOUNDARY] %s evolved (%s): reads={%s} writes={%s} (was reads={%s} writes={%s})",
		subIdentity, reason, strings.Join(newReads, ", "), strings.Join(newWrites, ", "),
		strings.Join(sub.Reads, ", "), strings.Join(sub.Writes, ", "))
	return true, nil
}

// reboundOnObjectiveChange re-evolves every cell's boundary when the objective text changes,
// since a new goal reshapes what each cell must read and write. Best-effort; returns the
// number of boundaries that changed.
func (g *Grower) reboundOnObjectiveChange(ctx context.Context, namespace, oldObjective, newObjective string) int {
	if strings.TrimSpace(oldObjective) == strings.TrimSpace(newObjective) {
		return 0
	}
	// The goal changed: re-derive the contract (fields the new goal needs may appear) and
	// re-sync the map from reality, THEN reconsider every cell's boundary against the new
	// objective — so a boundary can actually grow to use a newly-added field.
	_, _ = g.EnsureContract(ctx, namespace)
	_ = g.RefreshAppMap(ctx, namespace)
	env := LoadEnvelope(g.ledger, namespace)
	if env == nil {
		return 0
	}
	reviewed, changed := 0, 0
	for _, sub := range env.SubsystemRequirements {
		if sub.Composition != nil {
			continue
		}
		reviewed++
		if ok, err := g.EvolveBoundary(ctx, namespace, sub.Identity, "objective-change"); err == nil && ok {
			changed++
		}
	}
	log.Printf("[BOUNDARY] %s objective changed — reviewed %d boundary(ies), %d changed", namespace, reviewed, changed)
	return changed
}

// boundaryPayload renders the model input for a boundary review: purpose, contract, current
// ports, verified access, the mismatch, and failing checks.
func boundaryPayload(sub Subsystem, comp *evolution.ComponentMap, contract *evolution.AppContract, reason string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "REASON FOR REVIEW: %s\n\n", reason)
	fmt.Fprintf(&b, "CELL PURPOSE: %s\n\n", sub.Semantics)
	b.WriteString("SHARED-STATE CONTRACT (the only fields that exist):\n")
	b.WriteString(contract.Render())
	b.WriteString("\n")
	fmt.Fprintf(&b, "CURRENT DECLARED BOUNDARY: reads={%s} writes={%s}\n",
		strings.Join(sub.Reads, ", "), strings.Join(sub.Writes, ", "))
	if comp != nil {
		fmt.Fprintf(&b, "WHAT ITS CODE + PASSING CHECKS TOUCH: reads={%s} writes={%s}\n",
			strings.Join(comp.Reads, ", "), strings.Join(comp.Writes, ", "))
		if fr := boundaryFriction(comp); !fr.None() {
			fmt.Fprintf(&b, "MISMATCH: undeclared-reads={%s} undeclared-writes={%s} unused-reads={%s} unused-writes={%s}\n",
				strings.Join(fr.UndeclaredReads, ", "), strings.Join(fr.UndeclaredWrites, ", "),
				strings.Join(fr.UnusedReads, ", "), strings.Join(fr.UnusedWrites, ", "))
		}
	}
	return b.String()
}

// groundFields keeps only names that resolve to a real contract field (or the literal "HMI
// input"), de-duplicated case-insensitively — so an evolved boundary can never grant access to
// a field that does not exist.
func groundFields(names []string, contract *evolution.AppContract) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		key := strings.ToLower(n)
		if seen[key] {
			continue
		}
		if strings.EqualFold(n, "HMI input") {
			seen[key] = true
			out = append(out, "HMI input")
			continue
		}
		if _, _, ok := contract.FieldRange(n); ok {
			seen[key] = true
			out = append(out, n)
		}
	}
	return out
}

// sortedLower returns the names lowercased, de-duplicated, and sorted — a stable form for
// fingerprinting a port set.
func sortedLower(names []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, n := range names {
		if n = strings.ToLower(strings.TrimSpace(n)); n != "" && !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

func fieldSet(names []string) map[string]bool {
	s := map[string]bool{}
	for _, n := range names {
		if n = strings.TrimSpace(n); n != "" {
			s[strings.ToLower(n)] = true
		}
	}
	return s
}

// minusSet returns members of a not present in b, in a stable order, original-cased from a's keys.
func minusSet(a, b map[string]bool) []string {
	var out []string
	for k := range a {
		if !b[k] {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func sameFieldSet(a, b []string) bool {
	sa, sb := fieldSet(a), fieldSet(b)
	if len(sa) != len(sb) {
		return false
	}
	for k := range sa {
		if !sb[k] {
			return false
		}
	}
	return true
}

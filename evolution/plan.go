package evolution

// The PLAN layer is the missing "how". Until now a cell was synthesized straight
// from a thin spec — a one-line semantics, its I/O ports, the contract, and a set
// of acceptance checks — so every synthesis REINVENTED the design, inconsistently,
// with only the checks as an anchor. A human engineer instead works from a plan: a
// design that says, step by step, what each component does and how the components
// connect. The code is an implementation of that plan.
//
// AppPlan / ComponentPlan make that design a first-class, persisted, EVOLVING
// artifact. It is authored up front from the objective + the declared component
// interfaces + the contract, refined as the system changes (fracture, stall,
// architecture growth), and injected — plan-first — into synthesis: the build
// prompt implements the component's plan, and the acceptance checks VERIFY it.
//
// Layering: contract (data) → PLAN (design) → code (implementation) → map
// (as-built). The gap between the plan (intended) and the map (verified) is the
// work that remains.

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mdcfrancis/flow/storage"
)

// ComponentPlan is the detailed design of one component.
type ComponentPlan struct {
	Identity string `json:"identity"`
	Purpose  string `json:"purpose"`         // what it does and why, in the system
	Entry    string `json:"entry,omitempty"` // run-tick | render-frame
	// Reads/Writes are the contract fields it consumes/produces (plus "HMI input").
	Reads  []string `json:"reads,omitempty"`
	Writes []string `json:"writes,omitempty"`
	// Steps is the ordered algorithm the code implements each tick/frame.
	Steps []string `json:"steps,omitempty"`
	// Interactions is the behavioral connectivity: which sibling components it
	// hands off to / receives from, through which shared fields.
	Interactions []string `json:"interactions,omitempty"`
	// Invariants are conditions that must always hold (bounds, flags, edge cases).
	Invariants []string `json:"invariants,omitempty"`
	// Notes accumulates refinements made as the system evolved (why the plan
	// changed), so the design carries its own history.
	Notes string `json:"notes,omitempty"`
}

// AppPlan is the system design: an overview, the per-tick/frame CHOREOGRAPHY that
// connects the components, and each component's detailed plan.
type AppPlan struct {
	Namespace    string          `json:"namespace"`
	Objective    string          `json:"objective"`
	Overview     string          `json:"overview"`
	Choreography []string        `json:"choreography,omitempty"`
	Components   []ComponentPlan `json:"components"`
}

func planRefURN(namespace string) string { return namespace + ":plan" }

// SavePlan persists an application's plan.
func SavePlan(ledger *storage.LedgerEngine, namespace string, p *AppPlan) error {
	raw, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("encode plan: %w", err)
	}
	h, err := ledger.WriteBlock(raw)
	if err != nil {
		return fmt.Errorf("persist plan: %w", err)
	}
	return ledger.UpdateRef(planRefURN(namespace), h)
}

// LoadPlan returns an application's plan, or nil if none exists.
func LoadPlan(ledger *storage.LedgerEngine, namespace string) *AppPlan {
	h, err := ledger.GetRef(planRefURN(namespace))
	if err != nil {
		return nil
	}
	raw, err := ledger.ReadBlock(h)
	if err != nil {
		return nil
	}
	var p AppPlan
	if json.Unmarshal(raw, &p) != nil {
		return nil
	}
	return &p
}

// Component returns the plan for one component, or nil.
func (p *AppPlan) Component(identity string) *ComponentPlan {
	if p == nil {
		return nil
	}
	for i := range p.Components {
		if p.Components[i].Identity == identity {
			return &p.Components[i]
		}
	}
	return nil
}

// Upsert merges a component plan (by identity), preserving accumulated Notes.
func (p *AppPlan) Upsert(c ComponentPlan) {
	for i := range p.Components {
		if p.Components[i].Identity == c.Identity {
			if c.Notes == "" {
				c.Notes = p.Components[i].Notes
			}
			p.Components[i] = c
			return
		}
	}
	p.Components = append(p.Components, c)
}

// Remove drops a component plan (e.g. a fractured parent).
func (p *AppPlan) Remove(identity string) {
	out := p.Components[:0]
	for _, c := range p.Components {
		if c.Identity != identity {
			out = append(out, c)
		}
	}
	p.Components = out
}

// RenderSystem formats the app-level design (overview + choreography + the roster
// of components) for a prompt — the connective tissue every cell should build to.
func (p *AppPlan) RenderSystem() string {
	if p == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("SYSTEM PLAN — the design your cell implements a part of:\n")
	if p.Overview != "" {
		fmt.Fprintf(&b, "%s\n", p.Overview)
	}
	if len(p.Choreography) > 0 {
		b.WriteString("Each tick/frame, the system does, in order:\n")
		for i, step := range p.Choreography {
			fmt.Fprintf(&b, "  %d. %s\n", i+1, step)
		}
	}
	if len(p.Components) > 0 {
		b.WriteString("Components:\n")
		for _, c := range p.Components {
			fmt.Fprintf(&b, "  - %s: %s\n", shortName(c.Identity), c.Purpose)
		}
	}
	return b.String()
}

// RenderComponent formats ONE component's detailed plan — the design the code is
// an implementation of. This is what buildSeed leads with.
func (c *ComponentPlan) Render() string {
	if c == nil {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "PLAN FOR THIS CELL (%s) — IMPLEMENT THIS DESIGN:\n", shortName(c.Identity))
	if c.Purpose != "" {
		fmt.Fprintf(&b, "Purpose: %s\n", c.Purpose)
	}
	if len(c.Reads) > 0 {
		fmt.Fprintf(&b, "Inputs (read these shared fields): %s\n", strings.Join(c.Reads, ", "))
	}
	if len(c.Writes) > 0 {
		fmt.Fprintf(&b, "Outputs (write these shared fields): %s\n", strings.Join(c.Writes, ", "))
	}
	if len(c.Steps) > 0 {
		b.WriteString("Algorithm — do these, in order, each call:\n")
		for i, s := range c.Steps {
			fmt.Fprintf(&b, "  %d. %s\n", i+1, s)
		}
	}
	if len(c.Interactions) > 0 {
		b.WriteString("Connects to siblings:\n")
		for _, s := range c.Interactions {
			fmt.Fprintf(&b, "  - %s\n", s)
		}
	}
	if len(c.Invariants) > 0 {
		b.WriteString("Must always hold:\n")
		for _, s := range c.Invariants {
			fmt.Fprintf(&b, "  - %s\n", s)
		}
	}
	if c.Notes != "" {
		fmt.Fprintf(&b, "Design notes: %s\n", c.Notes)
	}
	return b.String()
}

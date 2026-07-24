package flux

// An Interface is a role a cell implicitly satisfies from its STRUCTURE — a
// typeclass, Go-style: there is no `implements` keyword, satisfaction is derived
// from the checked Cell (its reads/writes/terminal). Each interface contributes an
// entry export to the lowered ABI (or none, for a pure capability marker) and
// carries capability implications the rest of the system honors (boundary,
// scenario seeding). A cell may satisfy one or more. See docs/flux-interfaces.md.
type Interface struct {
	Name  string // "tick" | "view" | "input-source" | "stateful"
	Entry string // exported function it contributes ("" = no export, a marker only)
	// Satisfied reports whether a checked cell plays this role. It runs over the
	// TYPED cell + its layout, so an input-source is recognized by a field's real
	// ReadOnly flag, never a name substring.
	Satisfied func(c *Cell, layout Layout) bool
	// NeedsHMIInput marks the capability implication used by the boundary and
	// scenario layers: a cell satisfying an interface with this set must declare the
	// "HMI input" boundary and have its read-only inputs seeded in its scenarios.
	NeedsHMIInput bool
}

// Registry is the ordered set of known interfaces. Order is stable so the primary
// entry (the first entry-bearing interface a cell satisfies) is deterministic.
var Registry = []Interface{
	{Name: "tick", Entry: "run-tick", Satisfied: func(c *Cell, _ Layout) bool { return c.Kind == KindCompute }},
	{Name: "view", Entry: "render-frame", Satisfied: func(c *Cell, _ Layout) bool { return c.Kind == KindView }},
	{Name: "input-source", Satisfied: readsReadOnly, NeedsHMIInput: true},
	{Name: "stateful", Satisfied: func(c *Cell, _ Layout) bool { return len(c.Writes) > 0 }},
}

// DeriveInterfaces returns the interfaces a checked cell implicitly satisfies, in
// Registry order. This is the single source of truth for "what roles does this
// cell play" — the lowerer, the entry contract, the boundary, and scenario
// grounding all read it instead of re-sniffing the cell's shape.
func DeriveInterfaces(c *Cell, layout Layout) []Interface {
	if c == nil {
		return nil
	}
	var out []Interface
	for _, iface := range Registry {
		if iface.Satisfied != nil && iface.Satisfied(c, layout) {
			out = append(out, iface)
		}
	}
	return out
}

// PrimaryEntry is the export the cell is lowered against: the first entry-bearing
// interface it satisfies. Every well-formed cell has exactly one (tick or view).
func PrimaryEntry(c *Cell, layout Layout) string {
	for _, iface := range DeriveInterfaces(c, layout) {
		if iface.Entry != "" {
			return iface.Entry
		}
	}
	return ""
}

// NeedsHMIInput reports whether the cell satisfies any interface that requires the
// "HMI input" capability boundary — i.e. it reads a host-written input register.
func NeedsHMIInput(c *Cell, layout Layout) bool {
	for _, iface := range DeriveInterfaces(c, layout) {
		if iface.NeedsHMIInput {
			return true
		}
	}
	return false
}

// readsReadOnly reports whether the cell reads any read-only (host-written) field —
// the structural mark of an input source (mouse, key, slider, …).
func readsReadOnly(c *Cell, layout Layout) bool {
	for _, r := range c.Reads {
		if f, ok := layout[r]; ok && f.ReadOnly {
			return true
		}
	}
	return false
}

// EntryOf derives a cell's entry export from its SOURCE alone, without a layout —
// a light path for callers (e.g. the evolution loop's entry-contract selection)
// that hold only the genome string. A cell that draws is a view (render-frame);
// otherwise it ticks (run-tick). Structural: it inspects the parse tree, not a
// substring.
func EntryOf(src string) (string, error) {
	f, err := Parse("cell", src)
	if err != nil {
		return "", err
	}
	var cell *List
	for _, form := range f.Forms {
		if form.Head() == "cell" {
			cell = form
			break
		}
	}
	if cell == nil {
		return "", errf("", "no (cell …) form in source")
	}
	if containsHead(cell, "draw") {
		return "render-frame", nil
	}
	return "run-tick", nil
}

// containsHead reports whether l or any nested list has the given head symbol — used
// to find a cell's terminal (a (draw …) marks a view) through its let-wrapping.
func containsHead(l *List, head string) bool {
	if l == nil {
		return false
	}
	if l.Head() == head {
		return true
	}
	for _, it := range l.Items {
		if it.List != nil && containsHead(it.List, head) {
			return true
		}
	}
	return false
}

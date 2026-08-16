package evolution

import (
	"encoding/json"
	"strings"

	"github.com/mdcfrancis/flow/flux"
	"github.com/mdcfrancis/flow/stdlib"
	"github.com/mdcfrancis/flow/storage"
)

// The application PROLOGUE is a per-app set of user-defined macros (docs: the macro
// surface). Each is a hygienic (defmacro …) form the flux expander understands. A
// built-in DEFAULT set (composite arithmetic/motion helpers) is available to every
// cell; an app accumulates its OWN macros as the model authors and commits cells that
// define new ones (harvested on commit). At synthesis the effective prologue is
// prepended to the model's program before expansion, and its signatures are shown in
// the build prompt — so a cell CALLS (reflect …)/(clampi …) like a primitive instead
// of re-deriving the inline WAT every time.
//
// This is the evolvable surface, reincarnated as a pure syntactic layer: a macro is a
// substitution over WAT, verified by the cell that uses it through the identical
// acceptance gate — no IR, no type system, no grammar.

// PrologueMacro is one prologue entry: its name, ordered parameters, and full
// (defmacro …) source text (the form flux.Expand consumes).
type PrologueMacro struct {
	Name   string   `json:"name"`
	Params []string `json:"params"`
	Src    string   `json:"src"`
	Doc    string   `json:"doc,omitempty"` // one-line human description for the prompt
}

func prologueRef(ns string) string { return ns + ":prologue" }

// LoadPrologue returns an app's stored (harvested) macros, or nil.
func LoadPrologue(ledger *storage.LedgerEngine, ns string) []PrologueMacro {
	if ledger == nil {
		return nil
	}
	h, err := ledger.GetRef(prologueRef(ns))
	if err != nil {
		return nil
	}
	raw, err := ledger.ReadBlock(h)
	if err != nil {
		return nil
	}
	var ms []PrologueMacro
	if json.Unmarshal(raw, &ms) != nil {
		return nil
	}
	return ms
}

// SavePrologue replaces an app's stored macro set.
func SavePrologue(ledger *storage.LedgerEngine, ns string, ms []PrologueMacro) error {
	if ledger == nil {
		return nil
	}
	raw, err := json.Marshal(ms)
	if err != nil {
		return err
	}
	h, err := ledger.WriteBlock(raw)
	if err != nil {
		return err
	}
	return ledger.UpdateRef(prologueRef(ns), h)
}

// DefaultPrologue is the built-in starter set of composite macros, available to every
// cell. They are loaded from the embedded stdlib/prologue source files (not inline Go
// strings); their names/params are read straight from the (defmacro …) body. These are
// i32 helpers (game state is usually integer pixels); an app evolves its own — including
// f32 variants — as cells define and commit them. Every default is asserted to
// expand + assemble by TestDefaultPrologueAssembles.
func DefaultPrologue() []PrologueMacro {
	src := stdlib.Prologue()
	out := make([]PrologueMacro, 0, len(src))
	for _, m := range src {
		defs, err := flux.ExtractDefmacros(m.Src)
		if err != nil || len(defs) == 0 {
			continue
		}
		d := defs[0]
		out = append(out, PrologueMacro{Name: d.Name, Params: d.Params, Src: d.Src, Doc: m.Doc})
	}
	return out
}

// EffectivePrologue is the DEFAULT set overlaid with the app's stored macros: an app
// macro of the same name overrides the default (so a cell can refine a helper).
func EffectivePrologue(ledger *storage.LedgerEngine, ns string) []PrologueMacro {
	byName := map[string]PrologueMacro{}
	order := []string{}
	add := func(m PrologueMacro) {
		if _, seen := byName[m.Name]; !seen {
			order = append(order, m.Name)
		}
		byName[m.Name] = m
	}
	for _, m := range DefaultPrologue() {
		add(m)
	}
	for _, m := range LoadPrologue(ledger, ns) {
		add(m)
	}
	out := make([]PrologueMacro, 0, len(order))
	for _, n := range order {
		out = append(out, byName[n])
	}
	return out
}

// PrologueText concatenates the macros' (defmacro …) source, for prepending to a
// model's program before flux.Expand — so a call to any of them resolves.
func PrologueText(ms []PrologueMacro) string {
	if len(ms) == 0 {
		return ""
	}
	var b strings.Builder
	for _, m := range ms {
		b.WriteString(m.Src)
		b.WriteByte('\n')
	}
	return b.String()
}

// HarvestPrologue promotes any NEW (defmacro …) the committed genome defined into the
// app's stored prologue, so sibling cells can reuse it. A new macro needs no extra
// verification: the committing cell already expanded + assembled + passed acceptance
// WITH it. A definition whose name already exists (a default or a prior app macro) is
// NOT promoted — the committing cell keeps its own inline copy, and existing cells are
// never silently rebound to a changed body (the conservative half of the redefinition
// gate). Best-effort; a parse failure just harvests nothing.
func HarvestPrologue(ledger *storage.LedgerEngine, ns, genome string) int {
	defs, err := flux.ExtractDefmacros(genome)
	if err != nil || len(defs) == 0 {
		return 0
	}
	known := map[string]bool{}
	for _, m := range DefaultPrologue() {
		known[m.Name] = true
	}
	app := LoadPrologue(ledger, ns)
	for _, m := range app {
		known[m.Name] = true
	}
	added := 0
	for _, d := range defs {
		if known[d.Name] {
			continue
		}
		app = append(app, PrologueMacro{Name: d.Name, Params: d.Params, Src: d.Src})
		known[d.Name] = true
		added++
	}
	if added > 0 {
		if err := SavePrologue(ledger, ns, app); err != nil {
			return 0
		}
	}
	return added
}

// PrologueGuide renders the available macros for the build prompt: each signature with
// its one-line description, so the model reuses them instead of re-deriving the WAT.
func PrologueGuide(ms []PrologueMacro) string {
	if len(ms) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\nAVAILABLE MACROS (call them like (get …); they are already defined for you — do NOT redefine):\n")
	for _, m := range ms {
		b.WriteString("  (")
		b.WriteString(m.Name)
		for _, p := range m.Params {
			b.WriteByte(' ')
			b.WriteString(p)
		}
		b.WriteByte(')')
		if m.Doc != "" {
			b.WriteString("  — ")
			b.WriteString(m.Doc)
		}
		b.WriteByte('\n')
	}
	b.WriteString("If you need a NEW reusable helper, define it once with (defmacro (NAME params…) BODY) before the cell; it will be remembered for sibling cells. A macro template may not declare a (local …).\n")
	return b.String()
}

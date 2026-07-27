package evolution

import (
	"encoding/json"
	"strings"

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
// cell. These are i32 helpers (game state is usually integer pixels); an app evolves
// its own — including f32 variants — as cells define and commit them. Every default is
// asserted to expand + assemble by TestDefaultPrologueAssembles.
func DefaultPrologue() []PrologueMacro {
	return []PrologueMacro{
		{Name: "mini", Params: []string{"a", "b"}, Doc: "the smaller of two i32 values",
			Src: "(defmacro (mini a b) (select a b (i32.lt_s a b)))"},
		{Name: "maxi", Params: []string{"a", "b"}, Doc: "the larger of two i32 values",
			Src: "(defmacro (maxi a b) (select a b (i32.gt_s a b)))"},
		{Name: "clampi", Params: []string{"x", "lo", "hi"}, Doc: "x clamped into [lo, hi]",
			Src: "(defmacro (clampi x lo hi) (maxi lo (mini x hi)))"},
		{Name: "integ", Params: []string{"p", "v"}, Doc: "advance position field p by velocity field v (p += v)",
			Src: "(defmacro (integ p v) (set p (i32.add (get p) (get v))))"},
		{Name: "reflect", Params: []string{"v", "p", "lo", "hi"},
			Doc: "if position field p is outside [lo, hi), negate velocity field v (a wall bounce)",
			Src: "(defmacro (reflect v p lo hi) (if (i32.or (i32.lt_s (get p) lo) (i32.ge_s (get p) hi)) (then (set v (i32.sub (i32.const 0) (get v))))))"},
	}
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

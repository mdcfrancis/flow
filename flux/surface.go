package flux

import (
	"crypto/sha256"
	"encoding/hex"
)

// THE FLUX ARCHITECTURE (docs/flux-surface-ir.md): three layers, one invariant.
//
//	surface text  ──Read──▶  *Cell (the IR)  ──Lower──▶  WAT
//	  EVOLVABLE                 INVARIANT          THE ONLY GO INVARIANT
//
// The Go layer's sole guarantee is Lower(*Cell) → WAT — the step that hides the ABI
// boilerplate (memory import, entry export, addressing) that motivated Flux over raw
// WAT. It never needs the grammar. So the SURFACE the model generates, and the parser
// that reads it, live ABOVE the IR and are free to evolve: any surface that Reads to
// an equivalent Cell lowers to the same WAT and so preserves external behavior BY
// CONSTRUCTION. This is what lets the science loop change the surface for LLM
// efficiency (tokens, canonicality, legibility) without risking behavior — the
// invariant gate is a structural WAT comparison, not a scenario re-run.
//
// A Surface is that evolvable front as a first-class, swappable pair: it Reads its
// concrete syntax into the IR and Renders the IR back to it. The default is the
// S-expression surface; a language experiment is a different Surface over the same IR.
type Surface interface {
	// Name identifies the surface (for scoreboards / lineage).
	Name() string
	// Read parses + type-checks concrete syntax into the invariant IR.
	Read(filename, src string, layout Layout) (*Cell, error)
	// Render serializes the IR back to this surface's concrete syntax.
	Render(c *Cell) string
}

// SExpr is the default (current) surface: the strict S-expression Flux the model
// authors today. Read = Parse + Check; Render is the inverse in render.go.
type SExpr struct{}

func (SExpr) Name() string { return "sexpr" }

func (SExpr) Read(filename, src string, layout Layout) (*Cell, error) {
	f, err := Parse(filename, src)
	if err != nil {
		return nil, err
	}
	return Check(f, layout)
}

func (SExpr) Render(c *Cell) string { return Render(c) }

// DefaultSurface is the surface the operational system uses. Swapping it (or reading
// a cell through an experimental Surface) is how a language change is trialed; the
// IR and Lower are untouched.
var DefaultSurface Surface = SExpr{}

// BehaviorHash is a cell's behavior identity: the hash of the WAT it lowers to.
// Because Lower is the sole invariant, two cells (from any surfaces) are
// behavior-equivalent iff their BehaviorHashes match. This is the science loop's
// cheap, structural invariant gate — "did this surface change preserve behavior?"
// becomes a hash comparison, no scenarios required (docs/language-evolution.md §2).
func BehaviorHash(c *Cell, layout Layout) (string, error) {
	wat, err := Lower(c, layout)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256([]byte(wat))
	return hex.EncodeToString(h[:]), nil
}

// SameBehavior reports whether two cells lower to identical WAT — the behavior
// invariant a surface experiment must hold.
func SameBehavior(a, b *Cell, layout Layout) bool {
	ha, ea := BehaviorHash(a, layout)
	hb, eb := BehaviorHash(b, layout)
	return ea == nil && eb == nil && ha == hb
}

package flux

import (
	"strings"
	"sync"
)

// THE PROLOGUE distils the language to its irreducible core. The invariant backend
// (Lower) hand-built a handful of COMPOSITE operators — neg, abs, min, max, clamp —
// out of the true machine primitives. Those are not primitives at all; they are
// DERIVATIONS. This file moves them out of the fixed Go lowerer and into a prologue
// of derivations expressed as data (Flux source over the core), which an expansion
// pass inlines to core IR before lowering. So:
//
//   - the enforced core shrinks to the ops that map ~1:1 to WASM (+ - * / mod, the
//     comparisons, and/or/not, if, let, read/write/draw), and
//   - the derived vocabulary becomes evolvable data the system can rewrite — the
//     first step of pushing the language onto the cell substrate (docs/lineage.md,
//     docs/flux-surface-ir.md). A new derivation (min, max, or a domain word like
//     `bounce`) is a data entry, not a change to the invariant lowerer.
//
// Expansion preserves behavior: each derivation lowers to the same core the hand-built
// version did (via if/select), so a cell using clamp is behaviorally identical.
type Derivation struct {
	Name   string
	Params []string
	Body   string // a Flux expression over the params, the core, and earlier derivations
}

// DefaultPrologue is the composite vocabulary, derived from the core. Order does not
// matter — expansion recurses to a fixpoint (clamp → max, min → if, <).
var DefaultPrologue = []Derivation{
	{"neg", []string{"x"}, "(- 0 x)"},
	{"abs", []string{"x"}, "(if (< x 0) (- 0 x) x)"},
	{"min", []string{"a", "b"}, "(if (< a b) a b)"},
	{"max", []string{"a", "b"}, "(if (< a b) b a)"},
	{"clamp", []string{"x", "lo", "hi"}, "(max lo (min x hi))"},
}

type derivTemplate struct {
	params []string
	body   Expr // typed expression with the params as free Vars
}

type prologue struct {
	templates map[string]*derivTemplate
}

// compilePrologue type-checks each derivation body into a reusable template. A
// derivation may reference core ops and earlier derivations; the checker types the
// latter as ordinary prims (they are inlined later by expansion).
func compilePrologue(ds []Derivation) (*prologue, error) {
	p := &prologue{templates: make(map[string]*derivTemplate, len(ds))}
	for _, d := range ds {
		layout := Layout{}
		var off uint32 = 0xC0000
		for _, pn := range d.Params {
			layout[pn] = Field{Type: TInt, Offset: off}
			off += 4
		}
		layout["__out"] = Field{Type: TInt, Offset: off}
		src := "(cell d (reads " + strings.Join(d.Params, " ") + ") (writes __out) (write (__out " + d.Body + ")))"
		cell, err := SExpr{}.Read("prologue:"+d.Name, src, layout)
		if err != nil {
			return nil, errf("", "prologue %q: %v", d.Name, err)
		}
		w, ok := cell.Body.(*Write)
		if !ok || len(w.Vals) != 1 {
			return nil, errf("", "prologue %q: body did not reduce to a single write", d.Name)
		}
		p.templates[d.Name] = &derivTemplate{params: d.Params, body: w.Vals[0]}
	}
	return p, nil
}

// expand returns a cell whose body contains only CORE operators — every derivation
// call inlined (recursively).
func (p *prologue) expand(c *Cell) (*Cell, error) {
	nb, err := p.expandExpr(c.Body)
	if err != nil {
		return nil, err
	}
	return &Cell{Name: c.Name, Kind: c.Kind, Reads: c.Reads, Writes: c.Writes, Body: nb}, nil
}

func (p *prologue) expandExpr(e Expr) (Expr, error) {
	switch x := e.(type) {
	case *Let:
		nv := make([]Expr, len(x.Vals))
		for i, v := range x.Vals {
			ev, err := p.expandExpr(v)
			if err != nil {
				return nil, err
			}
			nv[i] = ev
		}
		nb, err := p.expandExpr(x.Body)
		if err != nil {
			return nil, err
		}
		return &Let{base: x.base, Names: x.Names, Vals: nv, Body: nb}, nil
	case *Write:
		nv := make([]Expr, len(x.Vals))
		for i, v := range x.Vals {
			ev, err := p.expandExpr(v)
			if err != nil {
				return nil, err
			}
			nv[i] = ev
		}
		return &Write{base: x.base, Fields: x.Fields, Vals: nv}, nil
	case *Draw:
		np := make([]DrawPrim, len(x.Prims))
		for i, pr := range x.Prims {
			na := make([]Expr, len(pr.Args))
			for j, a := range pr.Args {
				ea, err := p.expandExpr(a)
				if err != nil {
					return nil, err
				}
				na[j] = ea
			}
			np[i] = DrawPrim{Op: pr.Op, Args: na, Pos: pr.Pos}
		}
		return &Draw{base: x.base, Prims: np}, nil
	case *Prim:
		na := make([]Expr, len(x.Args))
		for i, a := range x.Args {
			ea, err := p.expandExpr(a)
			if err != nil {
				return nil, err
			}
			na[i] = ea
		}
		if tmpl, ok := p.templates[x.Op]; ok {
			if len(na) != len(tmpl.params) {
				return nil, errf(x.pos(), "prologue %q expects %d args, got %d", x.Op, len(tmpl.params), len(na))
			}
			sub := make(map[string]Expr, len(na))
			for i, pn := range tmpl.params {
				sub[pn] = na[i]
			}
			return p.expandExpr(substitute(tmpl.body, sub)) // template may itself contain derivations
		}
		return &Prim{base: x.base, Op: x.Op, Args: na}, nil
	default:
		return e, nil // Var and literals are immutable leaves
	}
}

// substitute deep-copies a template body, replacing each param Var with its argument.
func substitute(e Expr, sub map[string]Expr) Expr {
	switch x := e.(type) {
	case *Var:
		if r, ok := sub[x.Name]; ok {
			return r
		}
		return x
	case *Prim:
		na := make([]Expr, len(x.Args))
		for i, a := range x.Args {
			na[i] = substitute(a, sub)
		}
		return &Prim{base: x.base, Op: x.Op, Args: na}
	default:
		return e
	}
}

var (
	prologueOnce sync.Once
	prologueInst *prologue
	prologueErr  error
)

// dfltPrologue compiles and caches the default prologue.
func dfltPrologue() (*prologue, error) {
	prologueOnce.Do(func() { prologueInst, prologueErr = compilePrologue(DefaultPrologue) })
	return prologueInst, prologueErr
}

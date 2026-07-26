package flux

import "strings"

// DATA-DRIVEN SURFACE: the last self-hosting step for the S-expression family
// (docs/flux-surface-ir.md). A surface no longer has to be a Go type — it can be a
// SurfaceSpec (data) that a single generic engine interprets. The spec renames the
// structural keywords (and, optionally, operators) and chooses whether the
// reads/writes clauses appear; everything else is the shared S-expr skeleton over the
// invariant IR. So a new surface in this family is DATA the ledger can hold and the
// system can evolve — the analog of the ledger-resident prologue.
//
// (The Forth surface stays an algorithmic Go Surface: a concatenative parser is not a
// keyword skin. This engine covers the prefix S-expr family.)
type SurfaceSpec struct {
	Name string `json:"name"`
	// Keyword maps a CANONICAL token (cell, reads, writes, write, let, draw, and any
	// operator) to the SKIN token the surface uses. Absent entries keep the canonical
	// token. Skin tokens must be valid Flux identifiers (the lexer charset
	// [A-Za-z0-9_+*/<>=!?-]) and distinct from field/local names (heads only are
	// remapped on read).
	Keyword map[string]string `json:"keyword,omitempty"`
	// DeriveClauses omits the (reads …)/(writes …) clauses; the checker derives them.
	DeriveClauses bool `json:"deriveClauses,omitempty"`
}

// SpecSurface is the generic engine that turns a SurfaceSpec into a Surface.
type SpecSurface struct{ Spec SurfaceSpec }

func (s SpecSurface) Name() string {
	if s.Spec.Name != "" {
		return s.Spec.Name
	}
	return "spec"
}

// Read parses the skinned S-expression, remaps its head keywords back to canonical
// (so the shared checker understands it), and type-checks into the IR.
func (s SpecSurface) Read(filename, src string, layout Layout) (*Cell, error) {
	f, err := Parse(filename, src)
	if err != nil {
		return nil, err
	}
	rev := make(map[string]string, len(s.Spec.Keyword))
	for canon, skin := range s.Spec.Keyword {
		rev[skin] = canon
	}
	for _, l := range f.Forms {
		unskinHeads(l, rev)
	}
	return Check(f, layout)
}

// unskinHeads rewrites each list's HEAD symbol from its skin token back to canonical
// (leaving non-head positions — fields, locals, literals — untouched).
func unskinHeads(l *List, rev map[string]string) {
	if l == nil {
		return
	}
	if len(l.Items) > 0 && l.Items[0].Atom != nil && l.Items[0].Atom.Symbol != nil {
		if canon, ok := rev[*l.Items[0].Atom.Symbol]; ok {
			*l.Items[0].Atom.Symbol = canon
		}
	}
	for _, it := range l.Items {
		if it.List != nil {
			unskinHeads(it.List, rev)
		}
	}
}

// Render serializes the IR into this surface's skinned S-expression.
func (s SpecSurface) Render(c *Cell) string {
	if c == nil {
		return ""
	}
	kw := func(canon string) string {
		if v, ok := s.Spec.Keyword[canon]; ok {
			return v
		}
		return canon
	}
	var b strings.Builder
	b.WriteString("(" + kw("cell") + " " + c.Name)
	if !s.Spec.DeriveClauses {
		b.WriteString(" (" + kw("reads") + " " + strings.Join(c.Reads, " ") + ")")
		if c.Kind == KindCompute {
			b.WriteString(" (" + kw("writes") + " " + strings.Join(c.Writes, " ") + ")")
		}
	}
	b.WriteByte(' ')
	b.WriteString(renderSpecExpr(c.Body, kw))
	b.WriteByte(')')
	return b.String()
}

func renderSpecExpr(e Expr, kw func(string) string) string {
	switch x := e.(type) {
	case *Prim:
		return "(" + kw(x.Op) + argsTailSpec(x.Args, kw) + ")"
	case *Let:
		var b strings.Builder
		b.WriteString("(" + kw("let") + " (")
		for i, n := range x.Names {
			if i > 0 {
				b.WriteByte(' ')
			}
			b.WriteString("[" + n + " " + renderSpecExpr(x.Vals[i], kw) + "]")
		}
		b.WriteString(") " + renderSpecExpr(x.Body, kw) + ")")
		return b.String()
	case *Write:
		var b strings.Builder
		b.WriteString("(" + kw("write"))
		for i, f := range x.Fields {
			b.WriteString(" (" + f + " " + renderSpecExpr(x.Vals[i], kw) + ")")
		}
		b.WriteByte(')')
		return b.String()
	case *Draw:
		var b strings.Builder
		b.WriteString("(" + kw("draw"))
		for _, p := range x.Prims {
			b.WriteString(" (" + kw(p.Op) + argsTailSpec(p.Args, kw) + ")")
		}
		b.WriteByte(')')
		return b.String()
	default:
		return renderLeaf(e) // Var and literals are surface-invariant
	}
}

func argsTailSpec(args []Expr, kw func(string) string) string {
	var b strings.Builder
	for _, a := range args {
		b.WriteByte(' ')
		b.WriteString(renderSpecExpr(a, kw))
	}
	return b.String()
}

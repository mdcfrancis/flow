package flux

import (
	"strconv"
	"strings"
)

// Check turns a parsed Flux file into a typed, checked Cell against a field
// Layout. It resolves every name, types every expression, and verifies each
// `write` targets a declared field of the matching type — so the lowerer that
// follows can assume a well-typed tree and never emit an invalid instruction.
func Check(f *File, layout Layout) (*Cell, error) {
	if f == nil || len(f.Forms) == 0 {
		return nil, errf("", "no cell form")
	}
	return checkCell(f.Forms[0], layout)
}

func checkCell(list *List, layout Layout) (*Cell, error) {
	if list.Head() != "cell" {
		return nil, errf(posOf(list), "top form must be (cell NAME …), got %q", list.Head())
	}
	if len(list.Items) < 3 {
		return nil, errf(posOf(list), "cell needs a name and a body")
	}
	name := symOf(list.Items[1])
	if name == "" {
		return nil, errf(posOf(list), "cell name must be a symbol")
	}
	c := &Cell{Name: name}

	// reads/writes clauses.
	scope := map[string]Type{}
	if reads := list.Sub("reads"); reads != nil {
		for _, it := range reads.Items[1:] {
			fn := symOf(it)
			fld, ok := layout[fn]
			if !ok {
				return nil, errf(posOf(reads), "read field %q is not in the shared-state layout", fn)
			}
			c.Reads = append(c.Reads, fn)
			scope[fn] = fld.Type
		}
	}
	writeSet := map[string]bool{}
	if writes := list.Sub("writes"); writes != nil {
		for _, it := range writes.Items[1:] {
			fn := symOf(it)
			if _, ok := layout[fn]; !ok {
				return nil, errf(posOf(writes), "write field %q is not in the shared-state layout", fn)
			}
			c.Writes = append(c.Writes, fn)
			writeSet[fn] = true
		}
	}

	// requires clause: monadic input capabilities. The system binds each to a
	// read-only field before check (its Alias is in the layout at a resource offset),
	// so a bound capability reads exactly like a declared read of a read-only field —
	// the cell names the capability, never the register. An unbound alias is an error
	// (the system failed to allocate a resource for it).
	caps, err := capabilitiesOf(list)
	if err != nil {
		return nil, err
	}
	for _, cap := range caps {
		fld, ok := layout[cap.Alias]
		if !ok {
			return nil, errf(cap.Pos, "capability %q is not bound in the layout — the system must allocate its resource before lowering", cap.Alias)
		}
		if !fld.ReadOnly {
			return nil, errf(cap.Pos, "capability %q must bind to a read-only resource", cap.Alias)
		}
		c.Requires = append(c.Requires, cap)
		c.Reads = append(c.Reads, cap.Alias) // loaded like any read; read-only enforced on write
		scope[cap.Alias] = fld.Type
	}

	// The body is the final item — a chain of lets ending in write/draw.
	bodySexp := list.Items[len(list.Items)-1]
	body, err := checkExpr(bodySexp, scope, layout, writeSet)
	if err != nil {
		return nil, err
	}
	c.Body = body
	switch terminalKind(body) {
	case "write":
		c.Kind = KindCompute
	case "draw":
		c.Kind = KindView
	default:
		return nil, errf(posOf(list), "cell body must end in a (write …) or (draw …)")
	}
	return c, nil
}

// terminalKind reports whether the body reduces (through lets) to a write or draw.
func terminalKind(e Expr) string {
	switch t := e.(type) {
	case *Write:
		return "write"
	case *Draw:
		return "draw"
	case *Let:
		return terminalKind(t.Body)
	default:
		return ""
	}
}

func checkExpr(s *Sexp, scope map[string]Type, layout Layout, writeSet map[string]bool) (Expr, error) {
	if s.Atom != nil {
		return checkAtom(s.Atom, scope)
	}
	l := s.List
	if l == nil || len(l.Items) == 0 {
		return nil, errf(posOf(l), "empty expression")
	}
	head := l.Head()
	switch head {
	case "let":
		return checkLet(l, scope, layout, writeSet)
	case "write":
		return checkWrite(l, scope, layout, writeSet)
	case "draw":
		return checkDraw(l, scope, layout, writeSet)
	case "if":
		return checkIf(l, scope, layout, writeSet)
	default:
		return checkPrim(l, scope, layout, writeSet)
	}
}

func checkAtom(a *Atom, scope map[string]Type) (Expr, error) {
	b := base{Pos: a.Pos.String()}
	switch {
	case a.Int != nil:
		n, err := strconv.ParseInt(*a.Int, 10, 32)
		if err != nil {
			return nil, errf(b.Pos, "bad Int literal %q", *a.Int)
		}
		b.Typ = TInt
		return &IntLit{base: b, V: int32(n)}, nil
	case a.Float != nil:
		f, err := strconv.ParseFloat(*a.Float, 32)
		if err != nil {
			return nil, errf(b.Pos, "bad Float literal %q", *a.Float)
		}
		b.Typ = TFloat
		return &FloatLit{base: b, V: float32(f)}, nil
	case a.Color != nil:
		n, err := strconv.ParseUint(strings.TrimPrefix(*a.Color, "#x"), 16, 64)
		if err != nil {
			return nil, errf(b.Pos, "bad Color literal %q", *a.Color)
		}
		b.Typ = TColor
		return &ColorLit{base: b, V: uint32(n)}, nil
	case a.Symbol != nil:
		name := *a.Symbol
		if name == "true" || name == "false" {
			b.Typ = TBool
			return &BoolLit{base: b, V: name == "true"}, nil
		}
		t, ok := scope[name]
		if !ok {
			return nil, errf(b.Pos, "unknown name %q (not a read field or let binding)", name)
		}
		b.Typ = t
		return &Var{base: b, Name: name}, nil
	default:
		return nil, errf(b.Pos, "unrecognized atom")
	}
}

func checkLet(l *List, scope map[string]Type, layout Layout, writeSet map[string]bool) (Expr, error) {
	if len(l.Items) != 3 {
		return nil, errf(posOf(l), "let must be (let ([name val]…) body)")
	}
	bindsList := l.Items[1].List
	if bindsList == nil {
		return nil, errf(posOf(l), "let bindings must be a list")
	}
	// Bindings share the outer scope for their values but extend it for the body
	// (sequential is fine for v1 — no binding references a later one).
	inner := map[string]Type{}
	for k, v := range scope {
		inner[k] = v
	}
	let := &Let{base: base{Pos: posOf(l)}}
	for _, bs := range bindsList.Items {
		if bs.List == nil || len(bs.List.Items) != 2 {
			return nil, errf(posOf(l), "each let binding must be (name val)")
		}
		nm := symOf(bs.List.Items[0])
		if nm == "" {
			return nil, errf(posOf(l), "let binding name must be a symbol")
		}
		val, err := checkExpr(bs.List.Items[1], inner, layout, writeSet)
		if err != nil {
			return nil, err
		}
		inner[nm] = val.T()
		let.Names = append(let.Names, nm)
		let.Vals = append(let.Vals, val)
	}
	body, err := checkExpr(l.Items[2], inner, layout, writeSet)
	if err != nil {
		return nil, err
	}
	let.Body = body
	let.Typ = body.T()
	return let, nil
}

func checkWrite(l *List, scope map[string]Type, layout Layout, writeSet map[string]bool) (Expr, error) {
	w := &Write{base: base{Pos: posOf(l), Typ: TUnit}}
	for _, it := range l.Items[1:] {
		if it.List == nil || len(it.List.Items) != 2 {
			return nil, errf(posOf(l), "each write entry must be (field value)")
		}
		fn := symOf(it.List.Items[0])
		fld, ok := layout[fn]
		if !ok {
			return nil, errf(posOf(it.List), "write field %q is not in the shared-state layout", fn)
		}
		if fld.ReadOnly {
			return nil, errf(posOf(it.List), "field %q is a read-only input (host-written); you may read it but not write it", fn)
		}
		if len(writeSet) > 0 && !writeSet[fn] {
			return nil, errf(posOf(it.List), "write to %q which is not in the cell's declared writes", fn)
		}
		val, err := checkExpr(it.List.Items[1], scope, layout, writeSet)
		if err != nil {
			return nil, err
		}
		if val.T() != fld.Type {
			return nil, errf(it.List.Pos.String(), "field %q is %s, but its value is %s", fn, fld.Type, val.T())
		}
		w.Fields = append(w.Fields, fn)
		w.Vals = append(w.Vals, val)
	}
	return w, nil
}

func checkDraw(l *List, scope map[string]Type, layout Layout, writeSet map[string]bool) (Expr, error) {
	d := &Draw{base: base{Pos: posOf(l), Typ: TUnit}}
	for _, it := range l.Items[1:] {
		p := it.List
		if p == nil {
			return nil, errf(posOf(l), "draw entries must be prims like (circle …)")
		}
		op := p.Head()
		want := map[string]int{"circle": 4, "rect": 5, "line": 5}[op] // args incl. color
		if want == 0 {
			return nil, errf(posOf(p), "unknown draw prim %q (circle|rect|line)", op)
		}
		if len(p.Items)-1 != want {
			return nil, errf(posOf(p), "%s takes %d args", op, want)
		}
		dp := DrawPrim{Op: op, Pos: posOf(p)}
		for i, a := range p.Items[1:] {
			ex, err := checkExpr(a, scope, layout, writeSet)
			if err != nil {
				return nil, err
			}
			// last arg is the Color; the rest are Int geometry.
			wantT := TInt
			if i == want-1 {
				wantT = TColor
			}
			if ex.T() != wantT {
				return nil, errf(ex.pos(), "%s arg %d is %s, want %s", op, i+1, ex.T(), wantT)
			}
			dp.Args = append(dp.Args, ex)
		}
		d.Prims = append(d.Prims, dp)
	}
	return d, nil
}

func checkIf(l *List, scope map[string]Type, layout Layout, writeSet map[string]bool) (Expr, error) {
	if len(l.Items) != 4 {
		return nil, errf(posOf(l), "if must be (if cond then else)")
	}
	cond, err := checkExpr(l.Items[1], scope, layout, writeSet)
	if err != nil {
		return nil, err
	}
	if cond.T() != TBool && cond.T() != TInt {
		return nil, errf(cond.pos(), "if condition is %s, want Bool", cond.T())
	}
	then, err := checkExpr(l.Items[2], scope, layout, writeSet)
	if err != nil {
		return nil, err
	}
	els, err := checkExpr(l.Items[3], scope, layout, writeSet)
	if err != nil {
		return nil, err
	}
	if then.T() != els.T() {
		return nil, errf(posOf(l), "if branches disagree: %s vs %s", then.T(), els.T())
	}
	return &Prim{base: base{Pos: posOf(l), Typ: then.T()}, Op: "if", Args: []Expr{cond, then, els}}, nil
}

// primSig gives each primitive's arity, the type its args must share, and its
// result type. A -1 arity means "1 arg" for unary forms handled below.
func checkPrim(l *List, scope map[string]Type, layout Layout, writeSet map[string]bool) (Expr, error) {
	op := l.Head()
	args := make([]Expr, 0, len(l.Items)-1)
	for _, it := range l.Items[1:] {
		ex, err := checkExpr(it, scope, layout, writeSet)
		if err != nil {
			return nil, err
		}
		args = append(args, ex)
	}
	pos := posOf(l)
	num := func(n int) error {
		if len(args) != n {
			return errf(pos, "%s takes %d args, got %d", op, n, len(args))
		}
		for _, a := range args {
			if a.T() != TInt {
				return errf(a.pos(), "%s expects Int args, got %s", op, a.T())
			}
		}
		return nil
	}
	mk := func(t Type) Expr { return &Prim{base: base{Pos: pos, Typ: t}, Op: op, Args: args} }
	switch op {
	case "+", "-", "*", "/", "mod", "min", "max":
		if err := num(2); err != nil {
			return nil, err
		}
		return mk(TInt), nil
	case "clamp":
		if err := num(3); err != nil {
			return nil, err
		}
		return mk(TInt), nil
	case "neg", "abs":
		if err := num(1); err != nil {
			return nil, err
		}
		return mk(TInt), nil
	case "<", "<=", ">", ">=", "=", "!=":
		if err := num(2); err != nil {
			return nil, err
		}
		return mk(TBool), nil
	case "and", "or":
		if len(args) != 2 || args[0].T() != TBool || args[1].T() != TBool {
			return nil, errf(pos, "%s takes 2 Bool args", op)
		}
		return mk(TBool), nil
	case "not":
		if len(args) != 1 || args[0].T() != TBool {
			return nil, errf(pos, "not takes 1 Bool arg")
		}
		return mk(TBool), nil
	default:
		return nil, errf(pos, "unknown primitive %q", op)
	}
}

// helpers ------------------------------------------------------------------

func symOf(s *Sexp) string {
	if s == nil || s.Atom == nil || s.Atom.Symbol == nil {
		return ""
	}
	return *s.Atom.Symbol
}

func posOf(l *List) string {
	if l == nil {
		return ""
	}
	return l.Pos.String()
}

func intOf(s *Sexp) (int32, error) {
	if s == nil || s.Atom == nil || s.Atom.Int == nil {
		return 0, errf("", "expected an integer")
	}
	n, err := strconv.ParseInt(*s.Atom.Int, 10, 32)
	if err != nil {
		return 0, err
	}
	return int32(n), nil
}

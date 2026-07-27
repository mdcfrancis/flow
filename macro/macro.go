// Package macro is the operational surface: a small HYGIENIC-MACRO layer over raw WAT.
// The model writes WAT — its native format, with full wasm (i32 AND f32) — but names
// shared-state fields instead of hand-computing offsets, and wraps the cell body in a
// short form instead of the module/import/function boilerplate. Expand() resolves those
// macros against the app contract and produces raw WAT for the existing assembler.
//
// This replaces the Flux/Forth language: the boilerplate FLUX removed (the module
// wrapper and the error-prone `(i32.load (i32.const 0xB0010))` offset arithmetic) is
// exactly what these macros remove — without a custom parser, type checker, or lowerer,
// and with native f32 (so continuous physics has no integer-division-underflow trap).
//
// Macros (everything else passes through as raw WAT):
//
//	(cell ENTRY BODY…)       → the module + memory import + (func (export "ENTRY") …).
//	                            ENTRY is run-tick (compute) or render-frame (UI). BODY is
//	                            the function body verbatim (locals first, then instrs,
//	                            ending in the i32 result the ABI returns).
//	(get NAME)               → load field NAME (f32.load if it's an f32 field, else i32).
//	(set NAME EXPR)          → store EXPR to field NAME (f32.store / i32.store by type).
//	(atidx NAME IDX)         → load element IDX of array field NAME (i32.load, ×4, +base).
//	(setidx NAME IDX EXPR)   → store EXPR to element IDX of array field NAME.
//	(field NAME)             → (i32.const OFFSET), the raw base offset of NAME.
package macro

import (
	"fmt"
	"strings"
)

// Field is a shared-state field's location and element type, from the app contract.
type Field struct {
	Offset uint32
	Float  bool // f32 (else i32); arrays are i32 elements
}

// Expand rewrites the macro forms in src against the field map and returns raw WAT ready
// for the assembler. src that is already a raw (module …) passes through unchanged.
func Expand(src string, fields map[string]Field) (string, error) {
	nodes, err := parse(src)
	if err != nil {
		return "", err
	}
	out := make([]node, 0, len(nodes))
	for _, n := range nodes {
		r, err := rewrite(n, fields)
		if err != nil {
			return "", err
		}
		out = append(out, r)
	}
	var b strings.Builder
	for i, n := range out {
		if i > 0 {
			b.WriteByte('\n')
		}
		serialize(&b, n, 0)
	}
	return b.String(), nil
}

// node is an s-expression: an atom (leaf, kids==nil) or a list.
type node struct {
	atom string
	kids []node
	list bool
}

func atom(s string) node     { return node{atom: s} }
func lst(k ...node) node     { return node{kids: k, list: true} }
func (n node) isAtom() bool  { return !n.list }
func (n node) head() string {
	if n.list && len(n.kids) > 0 && n.kids[0].isAtom() {
		return n.kids[0].atom
	}
	return ""
}

// rewrite expands macros bottom-up: children first (so a (set x (get y)) resolves its
// inner (get) before the outer (set)), then the node itself if its head is a macro.
func rewrite(n node, fields map[string]Field) (node, error) {
	if n.isAtom() {
		return n, nil
	}
	kids := make([]node, len(n.kids))
	for i, k := range n.kids {
		r, err := rewrite(k, fields)
		if err != nil {
			return node{}, err
		}
		kids[i] = r
	}
	n.kids = kids

	fieldOff := func(name string) (Field, error) {
		f, ok := fields[name]
		if !ok {
			return Field{}, fmt.Errorf("unknown field %q (not in the contract)", name)
		}
		return f, nil
	}
	off := func(o uint32) node { return lst(atom("i32.const"), atom(fmt.Sprintf("0x%X", o))) }

	switch n.head() {
	case "get":
		if len(n.kids) != 2 || !n.kids[1].isAtom() {
			return node{}, fmt.Errorf("(get NAME) takes one field name")
		}
		f, err := fieldOff(n.kids[1].atom)
		if err != nil {
			return node{}, err
		}
		ld := "i32.load"
		if f.Float {
			ld = "f32.load"
		}
		return lst(atom(ld), off(f.Offset)), nil

	case "set":
		if len(n.kids) != 3 || !n.kids[1].isAtom() {
			return node{}, fmt.Errorf("(set NAME EXPR) takes a field name and a value")
		}
		f, err := fieldOff(n.kids[1].atom)
		if err != nil {
			return node{}, err
		}
		st := "i32.store"
		if f.Float {
			st = "f32.store"
		}
		return lst(atom(st), off(f.Offset), n.kids[2]), nil

	case "field":
		if len(n.kids) != 2 || !n.kids[1].isAtom() {
			return node{}, fmt.Errorf("(field NAME) takes one field name")
		}
		f, err := fieldOff(n.kids[1].atom)
		if err != nil {
			return node{}, err
		}
		return off(f.Offset), nil

	case "atidx":
		// (atidx NAME IDX) → (i32.load (i32.add (i32.const base) (i32.mul IDX (i32.const 4))))
		if len(n.kids) != 3 || !n.kids[1].isAtom() {
			return node{}, fmt.Errorf("(atidx NAME IDX) takes a field name and an index")
		}
		f, err := fieldOff(n.kids[1].atom)
		if err != nil {
			return node{}, err
		}
		addr := lst(atom("i32.add"), off(f.Offset), lst(atom("i32.mul"), n.kids[2], lst(atom("i32.const"), atom("4"))))
		return lst(atom("i32.load"), addr), nil

	case "setidx":
		if len(n.kids) != 4 || !n.kids[1].isAtom() {
			return node{}, fmt.Errorf("(setidx NAME IDX EXPR) takes a field name, index, value")
		}
		f, err := fieldOff(n.kids[1].atom)
		if err != nil {
			return node{}, err
		}
		addr := lst(atom("i32.add"), off(f.Offset), lst(atom("i32.mul"), n.kids[2], lst(atom("i32.const"), atom("4"))))
		return lst(atom("i32.store"), addr, n.kids[3]), nil

	case "cell":
		// (cell ENTRY BODY…) → module + memory import + exported function.
		if len(n.kids) < 2 || !n.kids[1].isAtom() {
			return node{}, fmt.Errorf("(cell ENTRY BODY…) needs an entry name (run-tick or render-frame)")
		}
		entry := n.kids[1].atom
		fn := []node{
			atom("func"),
			lst(atom("export"), atom(`"`+entry+`"`)),
			lst(atom("param"), atom("i32"), atom("i32")),
			lst(atom("result"), atom("i32")),
		}
		fn = append(fn, n.kids[2:]...)
		return lst(
			atom("module"),
			lst(atom("import"), atom(`"hdm:kernel/hardware-io"`), atom(`"shared-cluster-memory"`), lst(atom("memory"), atom("100"))),
			lst(fn...),
		), nil
	}
	return n, nil
}

// --- s-expression parser (tolerant of ;; line and (; ;) block comments) ---

func parse(src string) ([]node, error) {
	toks := tokenize(src)
	var out []node
	i := 0
	for i < len(toks) {
		n, ni, err := parseNode(toks, i)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
		i = ni
	}
	return out, nil
}

func parseNode(toks []string, i int) (node, int, error) {
	if i >= len(toks) {
		return node{}, i, fmt.Errorf("unexpected end of input")
	}
	if toks[i] == "(" {
		i++
		var kids []node
		for i < len(toks) && toks[i] != ")" {
			n, ni, err := parseNode(toks, i)
			if err != nil {
				return node{}, i, err
			}
			kids = append(kids, n)
			i = ni
		}
		if i >= len(toks) {
			return node{}, i, fmt.Errorf("missing )")
		}
		return node{kids: kids, list: true}, i + 1, nil
	}
	if toks[i] == ")" {
		return node{}, i, fmt.Errorf("unexpected )")
	}
	return atom(toks[i]), i + 1, nil
}

func tokenize(src string) []string {
	var toks []string
	i, n := 0, len(src)
	for i < n {
		c := src[i]
		switch {
		case c == ';' && i+1 < n && src[i+1] == ';': // ;; line comment
			for i < n && src[i] != '\n' {
				i++
			}
		case c == '(' && i+1 < n && src[i+1] == ';': // (; block comment ;)
			i += 2
			for i+1 < n && !(src[i] == ';' && src[i+1] == ')') {
				i++
			}
			i += 2
		case c == '(' || c == ')':
			toks = append(toks, string(c))
			i++
		case c == '"': // string atom — keep the quotes
			j := i + 1
			for j < n && src[j] != '"' {
				if src[j] == '\\' {
					j++
				}
				j++
			}
			toks = append(toks, src[i:min(j+1, n)])
			i = j + 1
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		default: // bare atom
			j := i
			for j < n {
				d := src[j]
				if d == ' ' || d == '\t' || d == '\n' || d == '\r' || d == '(' || d == ')' || d == '"' {
					break
				}
				j++
			}
			toks = append(toks, src[i:j])
			i = j
		}
	}
	return toks
}

func serialize(b *strings.Builder, n node, depth int) {
	if n.isAtom() {
		b.WriteString(n.atom)
		return
	}
	b.WriteByte('(')
	for i, k := range n.kids {
		if i > 0 {
			b.WriteByte(' ')
		}
		serialize(b, k, depth+1)
	}
	b.WriteByte(')')
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// The macro surface is the operational synthesis surface: a small HYGIENIC-MACRO
// layer over raw WAT. The model writes WAT — its native format, with full wasm
// (i32 AND f32) — but names shared-state fields instead of hand-computing offsets,
// and wraps the cell body in a short form instead of the module/import/function
// boilerplate. Expand() resolves those macros against the app Layout and produces
// raw WAT for the assembler.
//
// This is what "flux" now means: the boilerplate the earlier Flux language removed
// (the module wrapper and the error-prone `(i32.load (i32.const 0xB0010))` offset
// arithmetic) is exactly what these macros remove — without a custom parser, type
// checker, or lowerer, and with native f32 (so continuous physics has no
// integer-division-underflow trap).
//
// Macros (everything else passes through as raw WAT):
//
//	(cell ENTRY BODY…)       → the module + memory import + (func (export "ENTRY") …).
//	                            ENTRY is run-tick (compute) or render-frame (UI). BODY is
//	                            the function body verbatim (locals first, then instrs,
//	                            ending in the i32 result the ABI returns).
//	(get NAME)               → load field NAME (f32.load if it's an f32 field, else i32).
//	(set NAME EXPR)          → store EXPR to field NAME (f32.store / i32.store by type).
//	(geti NAME)              → field NAME as an i32 (an f32 field is truncated).
//	(atidx NAME IDX)         → load element IDX of array field NAME (i32.load, ×4, +base).
//	(setidx NAME IDX EXPR)   → store EXPR to element IDX of array field NAME.
//	(field NAME)             → (i32.const OFFSET), the raw base offset of NAME.
//	(scene PRIM…)            → a render body: one 24-byte draw record per prim.
package flux

import (
	"fmt"
	"strings"
)

// Expand rewrites the macro forms in src against the field Layout and returns raw WAT
// ready for the assembler. src that is already a raw (module …) passes through
// unchanged. A field's element type is read straight from the Layout: TFloat fields
// load/store as f32, everything else as i32.
func Expand(src string, fields Layout) (string, error) {
	nodes, err := mparse(src)
	if err != nil {
		return "", err
	}
	// First pass: collect any (defmacro …) definitions (an application prologue can be
	// prepended as defmacro text, or a cell can define its own inline). They are removed
	// from the output; only the (cell …)/(module …) forms remain.
	macros := map[string]macroDef{}
	body := make([]mnode, 0, len(nodes))
	for _, n := range nodes {
		if n.head() == "defmacro" {
			name, def, derr := parseDefmacro(n)
			if derr != nil {
				return "", derr
			}
			macros[name] = def
			continue
		}
		body = append(body, n)
	}
	out := make([]mnode, 0, len(body))
	for _, n := range body {
		r, err := rewrite(n, fields, macros, 0)
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
		mserialize(&b, n, 0)
	}
	return b.String(), nil
}

// macroDef is a user-defined hygienic macro: its ordered parameter names and its
// template body. A call (NAME arg…) is expanded by substituting each arg node for its
// parameter atom throughout the template, then rewriting the result. v1 templates
// introduce no (local …) bindings, so substitution is capture-free by construction.
type macroDef struct {
	params []string
	body   mnode
}

// maxMacroDepth bounds nested user-macro expansion so a self-referential definition
// cannot loop forever.
const maxMacroDepth = 64

// parseDefmacro reads (defmacro (NAME p1 p2 …) BODY): the signature list names the
// macro and its parameters, BODY is the single template form. It rejects a template
// that declares a (local …) — a v1 restriction that keeps expansion capture-free
// (a macro that needs a temp would have to hoist it like (scene), a later extension).
func parseDefmacro(n mnode) (string, macroDef, error) {
	if len(n.kids) != 3 || !n.kids[1].list || len(n.kids[1].kids) == 0 || !n.kids[1].kids[0].isAtom() {
		return "", macroDef{}, fmt.Errorf("(defmacro (NAME params…) BODY) is malformed")
	}
	sig := n.kids[1]
	name := sig.kids[0].atom
	params := make([]string, 0, len(sig.kids)-1)
	for _, p := range sig.kids[1:] {
		if !p.isAtom() {
			return "", macroDef{}, fmt.Errorf("macro %q: parameters must be plain names", name)
		}
		params = append(params, p.atom)
	}
	body := n.kids[2]
	if declaresLocal(body) {
		return "", macroDef{}, fmt.Errorf("macro %q: templates may not declare (local …) (v1)", name)
	}
	return name, macroDef{params: params, body: body}, nil
}

// declaresLocal reports whether any node in the template is a (local …) declaration.
func declaresLocal(n mnode) bool {
	if n.isAtom() {
		return false
	}
	if n.head() == "local" {
		return true
	}
	for _, k := range n.kids {
		if declaresLocal(k) {
			return true
		}
	}
	return false
}

// subst returns a copy of the template with each parameter atom replaced by the
// argument node bound to it. A parameter may be substituted by a full sub-expression
// (e.g. (get screen_w)), so substitution is node-level, not textual.
func subst(n mnode, binding map[string]mnode) mnode {
	if n.isAtom() {
		if r, ok := binding[n.atom]; ok {
			return r
		}
		return n
	}
	kids := make([]mnode, len(n.kids))
	for i, k := range n.kids {
		kids[i] = subst(k, binding)
	}
	return mnode{kids: kids, list: true}
}

// Format pretty-prints an s-expression genome (macro-WAT or raw WAT) with indentation:
// a form that fits on one line stays inline; a longer one breaks with its head on the
// opening line and each argument on its own indented line. Non-s-expr input is returned
// unchanged, so it is safe to call on any stored genome.
func Format(src string) string {
	if !strings.HasPrefix(strings.TrimSpace(src), "(") {
		return strings.TrimSpace(src) // not an s-expr (plain text)
	}
	nodes, err := mparse(src)
	if err != nil || len(nodes) == 0 {
		return strings.TrimSpace(src)
	}
	var b strings.Builder
	for i, n := range nodes {
		if i > 0 {
			b.WriteString("\n\n")
		}
		mformat(&b, n, 0)
	}
	return b.String()
}

const fmtWidth = 72

func mformat(b *strings.Builder, n mnode, indent int) {
	if n.isAtom() {
		b.WriteString(n.atom)
		return
	}
	if one := oneLine(n); len(one)+indent*2 <= fmtWidth {
		b.WriteString(one)
		return
	}
	pad := strings.Repeat("  ", indent+1)
	b.WriteByte('(')
	for i, k := range n.kids {
		if i == 0 {
			mformat(b, k, indent+1) // head stays on the opening line
			continue
		}
		b.WriteByte('\n')
		b.WriteString(pad)
		mformat(b, k, indent+1)
	}
	b.WriteByte(')')
}

func oneLine(n mnode) string {
	if n.isAtom() {
		return n.atom
	}
	var b strings.Builder
	mserialize(&b, n, 0)
	return b.String()
}

// mnode is a macro s-expression: an atom (leaf, kids==nil) or a list. Named to avoid
// colliding with the Flux language AST while both live in this package during teardown.
type mnode struct {
	atom string
	kids []mnode
	list bool
}

func matom(s string) mnode    { return mnode{atom: s} }
func mlst(k ...mnode) mnode   { return mnode{kids: k, list: true} }
func (n mnode) isAtom() bool  { return !n.list }
func (n mnode) head() string {
	if n.list && len(n.kids) > 0 && n.kids[0].isAtom() {
		return n.kids[0].atom
	}
	return ""
}

// rewrite expands macros bottom-up: children first (so a (set x (get y)) resolves its
// inner (get) before the outer (set)), then the node itself if its head is a macro.
// A USER macro (one named in `macros`) is expanded top-down instead: its raw arguments
// are substituted into its template, which is then rewritten — so a macro may itself
// use (get)/(set)/other macros. depth bounds nested user-macro expansion.
func rewrite(n mnode, fields Layout, macros map[string]macroDef, depth int) (mnode, error) {
	if n.isAtom() {
		return n, nil
	}
	// User-defined macro: substitute args into the template and rewrite the expansion.
	if def, ok := macros[n.head()]; ok {
		if depth >= maxMacroDepth {
			return mnode{}, fmt.Errorf("macro %q expanded too deeply (recursive definition?)", n.head())
		}
		args := n.kids[1:]
		if len(args) != len(def.params) {
			return mnode{}, fmt.Errorf("macro %q takes %d argument(s), got %d", n.head(), len(def.params), len(args))
		}
		binding := make(map[string]mnode, len(def.params))
		for i, p := range def.params {
			binding[p] = args[i]
		}
		return rewrite(subst(def.body, binding), fields, macros, depth+1)
	}
	kids := make([]mnode, 0, len(n.kids))
	for _, k := range n.kids {
		r, err := rewrite(k, fields, macros, depth)
		if err != nil {
			return mnode{}, err
		}
		// A macro that expands to MANY instructions returns a (__splice …); flatten it
		// into this list so e.g. (scene …) becomes a run of stores in the function body.
		if r.list && r.head() == "__splice" {
			kids = append(kids, r.kids[1:]...)
		} else {
			kids = append(kids, r)
		}
	}
	n.kids = kids

	fieldOff := func(name string) (Field, error) {
		f, ok := fields[name]
		if !ok {
			return Field{}, fmt.Errorf("unknown field %q (not in the contract)", name)
		}
		return f, nil
	}
	off := func(o uint32) mnode { return mlst(matom("i32.const"), matom(fmt.Sprintf("0x%X", o))) }

	switch n.head() {
	case "get":
		if len(n.kids) != 2 || !n.kids[1].isAtom() {
			return mnode{}, fmt.Errorf("(get NAME) takes one field name")
		}
		f, err := fieldOff(n.kids[1].atom)
		if err != nil {
			return mnode{}, err
		}
		ld := "i32.load"
		if f.Type == TFloat {
			ld = "f32.load"
		}
		return mlst(matom(ld), off(f.Offset)), nil

	case "set":
		if len(n.kids) != 3 || !n.kids[1].isAtom() {
			return mnode{}, fmt.Errorf("(set NAME EXPR) takes a field name and a value")
		}
		f, err := fieldOff(n.kids[1].atom)
		if err != nil {
			return mnode{}, err
		}
		st := "i32.store"
		if f.Type == TFloat {
			st = "f32.store"
		}
		return mlst(matom(st), off(f.Offset), n.kids[2]), nil

	case "geti":
		// (geti NAME) → the field's value as an i32: an f32 field is truncated (for pixel
		// coords), an i32 field loaded directly.
		if len(n.kids) != 2 || !n.kids[1].isAtom() {
			return mnode{}, fmt.Errorf("(geti NAME) takes one field name")
		}
		f, err := fieldOff(n.kids[1].atom)
		if err != nil {
			return mnode{}, err
		}
		if f.Type == TFloat {
			return mlst(matom("i32.trunc_f32_s"), mlst(matom("f32.load"), off(f.Offset))), nil
		}
		return mlst(matom("i32.load"), off(f.Offset)), nil

	case "scene":
		// (scene PRIM…) is a render body: it writes each prim as a 24-byte draw record
		// starting at the base pointer (param 0) and returns the total byte length. Prims:
		//   (circle X Y R COLOR) (rect X Y W H COLOR) (line X1 Y1 X2 Y2 COLOR)
		return expandScene(n.kids[1:])

	case "field":
		if len(n.kids) != 2 || !n.kids[1].isAtom() {
			return mnode{}, fmt.Errorf("(field NAME) takes one field name")
		}
		f, err := fieldOff(n.kids[1].atom)
		if err != nil {
			return mnode{}, err
		}
		return off(f.Offset), nil

	case "atidx":
		// (atidx NAME IDX) → (i32.load (i32.add (i32.const base) (i32.mul IDX (i32.const 4))))
		if len(n.kids) != 3 || !n.kids[1].isAtom() {
			return mnode{}, fmt.Errorf("(atidx NAME IDX) takes a field name and an index")
		}
		f, err := fieldOff(n.kids[1].atom)
		if err != nil {
			return mnode{}, err
		}
		addr := mlst(matom("i32.add"), off(f.Offset), mlst(matom("i32.mul"), n.kids[2], mlst(matom("i32.const"), matom("4"))))
		return mlst(matom("i32.load"), addr), nil

	case "setidx":
		if len(n.kids) != 4 || !n.kids[1].isAtom() {
			return mnode{}, fmt.Errorf("(setidx NAME IDX EXPR) takes a field name, index, value")
		}
		f, err := fieldOff(n.kids[1].atom)
		if err != nil {
			return mnode{}, err
		}
		addr := mlst(matom("i32.add"), off(f.Offset), mlst(matom("i32.mul"), n.kids[2], mlst(matom("i32.const"), matom("4"))))
		return mlst(matom("i32.store"), addr, n.kids[3]), nil

	case "cell":
		// (cell ENTRY BODY…) → module + memory import + exported function.
		if len(n.kids) < 2 || !n.kids[1].isAtom() {
			return mnode{}, fmt.Errorf("(cell ENTRY BODY…) needs an entry name (run-tick or render-frame)")
		}
		entry := n.kids[1].atom
		fn := []mnode{
			matom("func"),
			mlst(matom("export"), matom(`"`+entry+`"`)),
			mlst(matom("param"), matom("i32"), matom("i32")),
			mlst(matom("result"), matom("i32")),
		}
		fn = append(fn, n.kids[2:]...)
		return mlst(
			matom("module"),
			mlst(matom("import"), matom(`"hdm:kernel/hardware-io"`), matom(`"shared-cluster-memory"`), mlst(matom("memory"), matom("100"))),
			mlst(fn...),
		), nil
	}
	return n, nil
}

// appLayer is the compositing layer app draws use; drawPrims maps a prim name to its
// opcode and how many coordinate args precede the color.
const appLayer = 1

var drawPrims = map[string]struct {
	op     int
	coords int
}{"rect": {1, 4}, "line": {2, 4}, "circle": {3, 3}}

// expandScene builds the render body: a draw pointer local initialized to the base
// param, one 24-byte record per prim, and a trailing (length = pointer - base). Returns
// a (__splice …) so the caller flattens it into the function body.
func expandScene(prims []mnode) (mnode, error) {
	const dp = "$__mdp"
	dpGet := mlst(matom("local.get"), matom(dp))
	out := []mnode{
		matom("__splice"),
		mlst(matom("local"), matom(dp), matom("i32")),
		mlst(matom("local.set"), matom(dp), mlst(matom("local.get"), matom("0"))),
	}
	store := func(slot int, val mnode) mnode {
		return mlst(matom("i32.store"),
			mlst(matom("i32.add"), dpGet, mlst(matom("i32.const"), matom(fmt.Sprintf("%d", slot*4)))),
			val)
	}
	zero := mlst(matom("i32.const"), matom("0"))
	for _, p := range prims {
		if !p.list || len(p.kids) == 0 || !p.kids[0].isAtom() {
			return mnode{}, fmt.Errorf("scene expects draw prims (circle/rect/line …)")
		}
		spec, ok := drawPrims[p.kids[0].atom]
		if !ok {
			return mnode{}, fmt.Errorf("unknown draw prim %q (want circle/rect/line)", p.kids[0].atom)
		}
		args := p.kids[1:]
		if len(args) != spec.coords+1 {
			return mnode{}, fmt.Errorf("(%s …) takes %d coords + a color", p.kids[0].atom, spec.coords)
		}
		abcd := []mnode{zero, zero, zero, zero}
		for i := 0; i < spec.coords; i++ {
			abcd[i] = args[i]
		}
		color := args[spec.coords]
		out = append(out,
			store(0, mlst(matom("i32.const"), matom(fmt.Sprintf("%d", (appLayer<<8)|spec.op)))),
			store(1, abcd[0]), store(2, abcd[1]), store(3, abcd[2]), store(4, abcd[3]),
			store(5, color),
			mlst(matom("local.set"), matom(dp), mlst(matom("i32.add"), dpGet, mlst(matom("i32.const"), matom("24")))),
		)
	}
	out = append(out, mlst(matom("i32.sub"), dpGet, mlst(matom("local.get"), matom("0"))))
	return mlst(out...), nil
}

// --- s-expression parser (tolerant of ;; line and (; ;) block comments) ---

func mparse(src string) ([]mnode, error) {
	toks := mtokenize(src)
	var out []mnode
	i := 0
	for i < len(toks) {
		n, ni, err := mparseNode(toks, i)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
		i = ni
	}
	return out, nil
}

func mparseNode(toks []string, i int) (mnode, int, error) {
	if i >= len(toks) {
		return mnode{}, i, fmt.Errorf("unexpected end of input")
	}
	if toks[i] == "(" {
		i++
		var kids []mnode
		for i < len(toks) && toks[i] != ")" {
			n, ni, err := mparseNode(toks, i)
			if err != nil {
				return mnode{}, i, err
			}
			kids = append(kids, n)
			i = ni
		}
		if i >= len(toks) {
			return mnode{}, i, fmt.Errorf("missing )")
		}
		return mnode{kids: kids, list: true}, i + 1, nil
	}
	if toks[i] == ")" {
		return mnode{}, i, fmt.Errorf("unexpected )")
	}
	return matom(toks[i]), i + 1, nil
}

func mtokenize(src string) []string {
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

func mserialize(b *strings.Builder, n mnode, depth int) {
	if n.isAtom() {
		b.WriteString(n.atom)
		return
	}
	b.WriteByte('(')
	for i, k := range n.kids {
		if i > 0 {
			b.WriteByte(' ')
		}
		mserialize(b, k, depth+1)
	}
	b.WriteByte(')')
}

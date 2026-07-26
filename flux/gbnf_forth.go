package flux

import (
	"fmt"
	"sort"
	"strings"
)

// forthMaxDepth bounds the expression (tree) depth of a single Forth statement — and
// therefore the STACK DEPTH during its evaluation (a depth-d postfix tree peaks at
// ~d+1 values on the stack). Keeping it small is the grammar-level realization of the
// local-introspection idea: anything deeper than this cannot be written inline, so
// the model MUST name it in the `=:` prologue, keeping every stack shallow and
// inspectable. It also keeps the grammar fully bounded (no runaway), like the s-expr
// GBNF.
const forthMaxDepth = 3

// GBNFForth is the guided grammar for the Forth surface at the default depth. A
// bounded, stack-balanced word stream: each statement is a depth-bounded postfix
// expression consumed by `=: local` (a prologue binding) or `-> field` (a write) or a
// draw word, so the stream is always balanced at statement boundaries. Atoms are the
// enumerated fields, a fixed local pool (t0…), and literals — no free identifiers.
func GBNFForth(layout Layout, kind CellKind) string {
	return gbnfForth(layout, kind, forthMaxDepth)
}

// gbnfForth is GBNFForth with an explicit depth bound, so a language experiment can
// A/B how TIGHT the bound is: a smaller maxDepth forbids deeper inline expressions,
// forcing more intermediates into the `=:` prologue — shallower, more inspectable
// stacks (the validity lever for the Forth surface, docs/flux-surface-ir.md).
func gbnfForth(layout Layout, kind CellKind, maxDepth int) string {
	if maxDepth < 1 {
		maxDepth = 1
	}
	var reads, writes []string
	for n, f := range layout {
		reads = append(reads, n)
		if !f.ReadOnly {
			writes = append(writes, n)
		}
	}
	sort.Strings(reads)
	sort.Strings(writes)
	if len(reads) == 0 || (kind == KindCompute && len(writes) == 0) {
		return ""
	}
	q := func(ns []string) string {
		qs := make([]string, len(ns))
		for i, n := range ns {
			qs[i] = fmt.Sprintf("%q", n)
		}
		return strings.Join(qs, " | ")
	}

	var b strings.Builder
	fmt.Fprintf(&b, "field ::= %s\n", q(reads))
	pool := make([]string, 8)
	for i := range pool {
		pool[i] = fmt.Sprintf("t%d", i)
	}
	fmt.Fprintf(&b, "local ::= %s\n", q(pool))
	b.WriteString(`int ::= "-"? [0-9] [0-9]{0,8}` + "\n")
	b.WriteString(`color ::= "#x" [0-9A-Fa-f]{2,8}` + "\n")
	b.WriteString(`atom ::= field | local | int | color | "true" | "false"` + "\n")
	b.WriteString(`binop ::= "+" | "-" | "*" | "/" | "mod" | "min" | "max" | "<=" | ">=" | "<" | ">" | "=" | "!=" | "and" | "or"` + "\n")
	b.WriteString(`unop ::= "neg" | "abs" | "not"` + "\n")
	// Stratified POSTFIX expression with exact per-op arities (binop=2, unop=1,
	// clamp/`?`=3) — the operator trails its operands. Depth-0 is an atom.
	b.WriteString("expr0 ::= atom\n")
	for d := 1; d <= maxDepth; d++ {
		p := fmt.Sprintf("expr%d", d-1)
		fmt.Fprintf(&b, "expr%d ::= atom | %s \" \" %s \" \" binop | %s \" \" unop | %s \" \" %s \" \" %s \" ?\" | %s \" \" %s \" \" %s \" clamp\"\n",
			d, p, p, p, p, p, p, p, p, p)
	}
	fmt.Fprintf(&b, "expr ::= expr%d\n", maxDepth)

	switch kind {
	case KindView:
		b.WriteString(`binding ::= expr " =: " local` + "\n")
		b.WriteString(`prim ::= expr " " expr " " expr " " color " circle" | expr " " expr " " expr " " expr " " color " rect" | expr " " expr " " expr " " expr " " color " line"` + "\n")
		fmt.Fprintf(&b, "root ::= (binding \" \"){0,%d} prim (\" \" prim){0,%d}\n", gbnfMaxList, gbnfMaxList)
	default: // compute
		fmt.Fprintf(&b, "writefield ::= %s\n", q(writes))
		b.WriteString(`binding ::= expr " =: " local` + "\n")
		b.WriteString(`write ::= expr " -> " writefield` + "\n")
		fmt.Fprintf(&b, "root ::= (binding \" \"){0,%d} write (\" \" write){0,%d}\n", gbnfMaxList, gbnfMaxList)
	}
	return b.String()
}

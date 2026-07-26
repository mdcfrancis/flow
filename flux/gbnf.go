package flux

import (
	"fmt"
	"sort"
	"strings"
)

// GBNF grammar-shape bounds. A guided decode must have NO unbounded path: a
// repetition-prone model will exploit any `*` or unbounded recursion into
// non-termination (observed: infinite write-pairs, infinite (clamp (abs …) nesting).
// So every list is capped and expression nesting is stratified to a fixed depth.
const (
	gbnfMaxArgs  = 3 // an op takes 1..gbnfMaxArgs+1 operands
	gbnfMaxDepth = 5 // expression nesting cap (physics needs ~3)
	gbnfMaxList  = 8 // reads / writes / write-pairs / draw-prims / let-bindings cap
)

// GBNF emits a grammar (vLLM/llama.cpp GBNF, passed as `guided_grammar`) that
// constrains a model to SYNTACTICALLY VALID Flux for one cell, specialized to its
// layout and kind. By construction: a well-formed (cell …) with the entry shape for
// the kind, reads/writes drawn only from the cell's actual fields, a closed
// primitive/draw-prim set, valid literals, and — crucially — a fully BOUNDED
// grammar (no runaway). Type correctness and logic remain the checker's and the
// scenarios' job. The cell name is a fixed constant (it carries no information, so
// the model never spends tokens on it). See docs/grammar-constrained-flux.md.
//
// Returns "" if there is nothing to constrain (no fields, or a compute cell with no
// writable field) — the caller then decodes unconstrained.
func GBNF(layout Layout, kind CellKind) string { return gbnf(layout, kind, false) }

// GBNFTerse is the EVOLVED, LLM-optimal grammar: identical, but it omits the
// (reads …)/(writes …) clauses — the checker derives them from the body. Fewer
// tokens and no possible clause/body mismatch, at equal validity. This is the first
// candidate language evolution measured against GBNF (see
// docs/grammar-constrained-flux.md).
func GBNFTerse(layout Layout, kind CellKind) string { return gbnf(layout, kind, true) }

func gbnf(layout Layout, kind CellKind, terse bool) string {
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
	alt := func(names []string) string {
		q := make([]string, len(names))
		for i, n := range names {
			q[i] = fmt.Sprintf("%q", n)
		}
		return strings.Join(q, " | ")
	}

	var b strings.Builder
	// No free identifiers anywhere: expression atoms are the ENUMERATED real fields,
	// a FIXED pool of let-names (t0…), and literals. So the model can neither invent a
	// field nor reference an unbound name — name-existence is guaranteed by
	// construction (not just syntax). This is what makes dropping the reads clause a
	// clean win.
	fmt.Fprintf(&b, "field ::= %s\n", alt(reads))
	pool := make([]string, 8)
	for i := range pool {
		pool[i] = fmt.Sprintf("t%d", i)
	}
	fmt.Fprintf(&b, "letname ::= %s\n", alt(pool))
	b.WriteString(`int ::= "-"? [0-9] [0-9]{0,8}` + "\n")
	b.WriteString(`color ::= "#x" [0-9A-Fa-f]{2,8}` + "\n")
	b.WriteString(`atom ::= field | letname | int | color | "true" | "false"` + "\n")
	b.WriteString(`binop ::= "+" | "-" | "*" | "/" | "mod" | "min" | "max" | "<=" | ">=" | "<" | ">" | "=" | "!=" | "and" | "or"` + "\n")
	b.WriteString(`unop ::= "neg" | "abs" | "not"` + "\n")
	// Stratified expression with EXACT per-op arities (binop=2, unop=1, clamp/if=3) —
	// so a wrong-arity form like (clamp 0 1) is ungrammatical, not merely a check
	// error. Depth-0 is an atom; depth-d nests depth-(d-1), capped at gbnfMaxDepth.
	b.WriteString("expr0 ::= atom\n")
	for d := 1; d <= gbnfMaxDepth; d++ {
		p := fmt.Sprintf("expr%d", d-1)
		fmt.Fprintf(&b, "expr%d ::= atom | \"(\" binop \" \" %s \" \" %s \")\" | \"(\" unop \" \" %s \")\" | \"(clamp \" %s \" \" %s \" \" %s \")\" | \"(if \" %s \" \" %s \" \" %s \")\"\n",
			d, p, p, p, p, p, p, p, p, p)
	}
	fmt.Fprintf(&b, "expr ::= expr%d\n", gbnfMaxDepth)
	// The reads clause is FIXED to every field (not a model-chosen subset): a
	// context-free grammar can't tie the atoms used in the body to a subset declared
	// in the clause, so declaring them all makes every field reference valid by
	// construction. (The redundancy of an explicit reads clause with the body is a
	// known LLM-optimal simplification — deriving reads/writes from the body — noted
	// in docs/grammar-constrained-flux.md.)
	readsLit := strings.Join(reads, " ")

	switch kind {
	case KindView:
		b.WriteString(`prim ::= "(circle " expr " " expr " " expr " " color ")" | "(rect " expr " " expr " " expr " " expr " " color ")" | "(line " expr " " expr " " expr " " expr " " color ")"` + "\n")
		fmt.Fprintf(&b, "draw ::= \"(draw \" prim (\" \" prim){0,%d} \")\"\n", gbnfMaxList)
		if terse {
			b.WriteString(`root ::= "(cell c " draw ")"` + "\n")
		} else {
			fmt.Fprintf(&b, "root ::= \"(cell c (reads %s) \" draw \")\"\n", readsLit)
		}
	default: // compute
		fmt.Fprintf(&b, "writefield ::= %s\n", alt(writes))
		b.WriteString(`pair ::= "(" writefield " " expr ")"` + "\n")
		fmt.Fprintf(&b, "write ::= \"(write \" pair (\" \" pair){0,%d} \")\"\n", gbnfMaxList)
		b.WriteString(`binding ::= "[" letname " " expr "]"` + "\n")
		// A body is a (write …), optionally wrapped in ONE let (multiple bindings) —
		// physics's shape; no nested lets, so still bounded.
		fmt.Fprintf(&b, "body ::= write | \"(let (\" binding (\" \" binding){0,%d} \") \" write \")\"\n", gbnfMaxList)
		if terse {
			b.WriteString(`root ::= "(cell c " body ")"` + "\n")
		} else {
			fmt.Fprintf(&b, "writes ::= writefield (\" \" writefield){0,%d}\n", gbnfMaxList)
			fmt.Fprintf(&b, "root ::= \"(cell c (reads %s) (writes \" writes \") \" body \")\"\n", readsLit)
		}
	}
	return b.String()
}

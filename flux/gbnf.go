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
func GBNF(layout Layout, kind CellKind) string {
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
	b.WriteString(`ident ::= [a-z] [a-zA-Z0-9_]{0,15}` + "\n")
	b.WriteString(`int ::= "-"? [0-9] [0-9]{0,8}` + "\n")
	b.WriteString(`color ::= "#x" [0-9A-Fa-f]{2,8}` + "\n")
	b.WriteString(`atom ::= ident | int | color | "true" | "false"` + "\n")
	b.WriteString(`op ::= "+" | "-" | "*" | "/" | "mod" | "neg" | "abs" | "min" | "max" | "clamp" | "if" | "<=" | ">=" | "<" | ">" | "=" | "!=" | "and" | "or" | "not"` + "\n")
	// Stratified expression: depth-0 is an atom; depth-d nests depth-(d-1). No
	// self-recursion, so nesting is capped at gbnfMaxDepth.
	b.WriteString("expr0 ::= atom\n")
	for d := 1; d <= gbnfMaxDepth; d++ {
		fmt.Fprintf(&b, "expr%d ::= atom | \"(\" op \" \" expr%d (\" \" expr%d){0,%d} \")\"\n", d, d-1, d-1, gbnfMaxArgs)
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
		fmt.Fprintf(&b, "root ::= \"(cell c (reads %s) \" draw \")\"\n", readsLit)
	default: // compute
		fmt.Fprintf(&b, "writefield ::= %s\n", alt(writes))
		fmt.Fprintf(&b, "writes ::= writefield (\" \" writefield){0,%d}\n", gbnfMaxList)
		b.WriteString(`pair ::= "(" writefield " " expr ")"` + "\n")
		fmt.Fprintf(&b, "write ::= \"(write \" pair (\" \" pair){0,%d} \")\"\n", gbnfMaxList)
		b.WriteString(`binding ::= "[" ident " " expr "]"` + "\n")
		// A body is a (write …), optionally wrapped in ONE let (multiple bindings) —
		// physics's shape; no nested lets, so still bounded.
		fmt.Fprintf(&b, "body ::= write | \"(let (\" binding (\" \" binding){0,%d} \") \" write \")\"\n", gbnfMaxList)
		fmt.Fprintf(&b, "root ::= \"(cell c (reads %s) (writes \" writes \") \" body \")\"\n", readsLit)
	}
	return b.String()
}

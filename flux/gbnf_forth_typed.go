package flux

import (
	"fmt"
	"sort"
	"strings"
)

// GBNFForthTyped is the TYPE-STRATIFIED Forth grammar: the residual Forth validity
// gap was type errors the (permissive) grammar admitted — a comparison's Bool flowing
// into arithmetic or into an Int field write. This grammar segregates expressions by
// type so those forms are UNGRAMMATICAL, not merely rejected by the checker:
//
//   - iexpr — Int-producing: int fields/locals/literals, arithmetic, clamp, min/max,
//     and (bexpr iexpr iexpr ?) — a select whose branches are Int.
//   - bexpr — Bool-producing: comparisons of iexprs, and/or/not, bool literals.
//   - color — only where a draw needs one.
//
// Local pools are typed too: `=: i0` binds an Int, `=: b0` a Bool, and iexpr/bexpr
// admit only their own pool — so a bound value keeps its type across uses. A write
// takes an iexpr into an Int field only. Type-correct by construction (docs/
// flux-surface-ir.md). Same depth bound as gbnfForth (shallow stacks).
func GBNFForthTyped(layout Layout, kind CellKind) string {
	return gbnfForthTyped(layout, kind, forthMaxDepth)
}

func gbnfForthTyped(layout Layout, kind CellKind, maxDepth int) string {
	if maxDepth < 1 {
		maxDepth = 1
	}
	var ifields, iwrites, cfields []string
	for n, f := range layout {
		switch f.Type {
		case TInt:
			ifields = append(ifields, n)
			if !f.ReadOnly {
				iwrites = append(iwrites, n)
			}
		case TColor:
			cfields = append(cfields, n)
		}
	}
	sort.Strings(ifields)
	sort.Strings(iwrites)
	sort.Strings(cfields)
	if len(ifields) == 0 || (kind == KindCompute && len(iwrites) == 0) {
		return ""
	}
	alt := func(ns []string) string {
		qs := make([]string, len(ns))
		for i, n := range ns {
			qs[i] = fmt.Sprintf("%q", n)
		}
		return strings.Join(qs, " | ")
	}

	var b strings.Builder
	fmt.Fprintf(&b, "ifield ::= %s\n", alt(ifields))
	b.WriteString(`ilocal ::= "i0" | "i1" | "i2" | "i3"` + "\n")
	b.WriteString(`blocal ::= "b0" | "b1" | "b2" | "b3"` + "\n")
	b.WriteString(`int ::= "-"? [0-9] [0-9]{0,8}` + "\n")
	b.WriteString(`color ::= "#x" [0-9A-Fa-f]{2,8}` + "\n")
	catom := "color"
	if len(cfields) > 0 {
		fmt.Fprintf(&b, "cfield ::= %s\n", alt(cfields))
		catom = "color | cfield"
	}
	fmt.Fprintf(&b, "catom ::= %s\n", catom)
	b.WriteString(`iatom ::= ifield | ilocal | int` + "\n")
	b.WriteString(`batom ::= blocal | "true" | "false"` + "\n")
	b.WriteString(`ibinop ::= "+" | "-" | "*" | "/" | "mod" | "min" | "max"` + "\n")
	b.WriteString(`cmp ::= "<=" | ">=" | "<" | ">" | "=" | "!="` + "\n")

	// Depth-stratified, by type. Each level admits its own base atom so lower levels
	// are subsumed (e.g. a bare bool local is a valid bexpr at any depth).
	b.WriteString("iexpr0 ::= iatom\n")
	b.WriteString("bexpr0 ::= batom\n")
	for d := 1; d <= maxDepth; d++ {
		ip := fmt.Sprintf("iexpr%d", d-1)
		bp := fmt.Sprintf("bexpr%d", d-1)
		// Int: atom | binop | neg/abs | clamp | if(select) with a Bool cond.
		fmt.Fprintf(&b, "iexpr%d ::= iatom | %s \" \" %s \" \" ibinop | %s \" neg\" | %s \" abs\" | %s \" \" %s \" \" %s \" clamp\" | %s \" \" %s \" \" %s \" ?\"\n",
			d, ip, ip, ip, ip, ip, ip, ip, bp, ip, ip)
		// Bool: atom | comparison of Ints | and/or of Bools | not.
		fmt.Fprintf(&b, "bexpr%d ::= batom | %s \" \" %s \" \" cmp | %s \" \" %s \" and\" | %s \" \" %s \" or\" | %s \" not\"\n",
			d, ip, ip, bp, bp, bp, bp, bp)
	}
	fmt.Fprintf(&b, "iexpr ::= iexpr%d\n", maxDepth)
	fmt.Fprintf(&b, "bexpr ::= bexpr%d\n", maxDepth)

	// A binding names an Int result to an i-local or a Bool result to a b-local.
	b.WriteString(`binding ::= iexpr " =: " ilocal | bexpr " =: " blocal` + "\n")

	switch kind {
	case KindView:
		b.WriteString(`prim ::= iexpr " " iexpr " " iexpr " " catom " circle" | iexpr " " iexpr " " iexpr " " iexpr " " catom " rect" | iexpr " " iexpr " " iexpr " " iexpr " " catom " line"` + "\n")
		fmt.Fprintf(&b, "root ::= (binding \" \"){0,%d} prim (\" \" prim){0,%d}\n", gbnfMaxList, gbnfMaxList)
	default: // compute
		fmt.Fprintf(&b, "iwrite ::= %s\n", alt(iwrites))
		b.WriteString(`write ::= iexpr " -> " iwrite` + "\n")
		fmt.Fprintf(&b, "root ::= (binding \" \"){0,%d} write (\" \" write){0,%d}\n", gbnfMaxList, gbnfMaxList)
	}
	return b.String()
}

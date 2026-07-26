package flux

import (
	"fmt"
	"strconv"
	"strings"
)

// Render is the INVERSE of Parse+Check: it serializes the invariant IR (a *Cell)
// back to Flux surface text. It is the missing half of the surface seam — with it,
// the surface is a genuine SERIALIZATION of the IR (text → Cell → text), which is
// what lets a language experiment be "a different surface for the same IR" and be
// checked for behavior-preservation structurally (docs/flux-surface-ir.md,
// docs/language-evolution.md). It is also how the system SHOWS the model an existing
// cell (examples, the current program during iteration) — the "processed by the LLM"
// half of the efficiency goal.
//
// Render targets the default S-expression surface. An evolved surface supplies its
// own renderer (a Surface, see surface.go); all must round-trip through the same IR.
func Render(c *Cell) string {
	if c == nil {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "(cell %s (reads %s)", c.Name, strings.Join(c.Reads, " "))
	if c.Kind == KindCompute {
		fmt.Fprintf(&b, " (writes %s)", strings.Join(c.Writes, " "))
	}
	b.WriteByte(' ')
	b.WriteString(renderExpr(c.Body))
	b.WriteByte(')')
	return b.String()
}

func renderExpr(e Expr) string {
	switch x := e.(type) {
	case *IntLit:
		return strconv.FormatInt(int64(x.V), 10)
	case *FloatLit:
		s := strconv.FormatFloat(float64(x.V), 'f', -1, 32)
		if !strings.ContainsRune(s, '.') {
			s += ".0" // the Float lexer requires a decimal point
		}
		return s
	case *BoolLit:
		if x.V {
			return "true"
		}
		return "false"
	case *ColorLit:
		return fmt.Sprintf("#x%08X", x.V)
	case *Var:
		return x.Name
	case *Prim:
		return "(" + x.Op + argsTail(x.Args) + ")"
	case *Let:
		var b strings.Builder
		b.WriteString("(let (")
		for i, n := range x.Names {
			if i > 0 {
				b.WriteByte(' ')
			}
			fmt.Fprintf(&b, "[%s %s]", n, renderExpr(x.Vals[i]))
		}
		b.WriteString(") ")
		b.WriteString(renderExpr(x.Body))
		b.WriteByte(')')
		return b.String()
	case *Write:
		var b strings.Builder
		b.WriteString("(write")
		for i, f := range x.Fields {
			fmt.Fprintf(&b, " (%s %s)", f, renderExpr(x.Vals[i]))
		}
		b.WriteByte(')')
		return b.String()
	case *Draw:
		var b strings.Builder
		b.WriteString("(draw")
		for _, p := range x.Prims {
			fmt.Fprintf(&b, " (%s%s)", p.Op, argsTail(p.Args))
		}
		b.WriteByte(')')
		return b.String()
	default:
		return ""
	}
}

// argsTail renders " arg arg …" (a leading space before each) for an operator or
// draw-primitive argument list.
func argsTail(args []Expr) string {
	var b strings.Builder
	for _, a := range args {
		b.WriteByte(' ')
		b.WriteString(renderExpr(a))
	}
	return b.String()
}

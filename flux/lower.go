package flux

import (
	"fmt"
	"strings"
)

// Lower turns a typed Cell into folded WAT text in the subset the HDM assembler
// accepts. It owns everything the model kept getting wrong: the module wrapper,
// the memory import, the entry export + signature, locals-at-top, the field
// load/store addressing, the draw-record ABI, and stack/type discipline. Because
// the input is already type-checked and the emission is deterministic, the WAT it
// produces cannot exhibit any of the compile-stage failure classes.
//
// v1 lowers the Int/Bool/Color core (all i32); Float (f32) is a later increment.
func Lower(c *Cell, layout Layout) (string, error) {
	if c == nil {
		return "", errf("", "nil cell")
	}
	var b strings.Builder
	b.WriteString("(module\n")
	b.WriteString("  (import \"hdm:kernel/hardware-io\" \"shared-cluster-memory\" (memory 100))\n")

	// The entry export is the cell's PRIMARY interface — derived from its shape, not
	// hardcoded here (see interface.go / docs/flux-interfaces.md).
	export := PrimaryEntry(c, layout)
	if export == "" {
		return "", errf("", "cell satisfies no entry interface (needs a write or draw terminal)")
	}
	fmt.Fprintf(&b, "  (func (export %q) (param $base i32) (param $cap i32) (result i32)\n", export)

	// Locals: one per read field, one per let binding (all i32 in v1), at the top.
	for _, n := range append(append([]string{}, c.Reads...), letNames(c.Body)...) {
		fmt.Fprintf(&b, "    (local $%s i32)\n", n)
	}

	// Load each read field from its offset into its local.
	for _, r := range c.Reads {
		fmt.Fprintf(&b, "    (local.set $%s (i32.load (i32.const 0x%X)))\n", r, layout[r].Offset)
	}

	// Emit the outer lets, then the terminal write/draw + the return value.
	term, err := lowerLets(&b, c.Body)
	if err != nil {
		return "", err
	}
	switch t := term.(type) {
	case *Write:
		for i, f := range t.Fields {
			v, err := lowerExpr(t.Vals[i])
			if err != nil {
				return "", err
			}
			fmt.Fprintf(&b, "    (i32.store (i32.const 0x%X) %s)\n", layout[f].Offset, v)
		}
		b.WriteString("    (i32.const 0))\n)\n") // run-tick returns 0
	case *Draw:
		if err := lowerDraw(&b, t); err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "    (i32.const %d))\n)\n", len(t.Prims)*24) // render-frame returns bytes written
	default:
		return "", errf("", "cell body did not reduce to write/draw")
	}
	return b.String(), nil
}

// lowerLets emits every outer let's bindings as local.set and returns the
// terminal expression (the write or draw) they wrap.
func lowerLets(b *strings.Builder, e Expr) (Expr, error) {
	for {
		lt, ok := e.(*Let)
		if !ok {
			return e, nil
		}
		for i, n := range lt.Names {
			v, err := lowerExpr(lt.Vals[i])
			if err != nil {
				return nil, err
			}
			fmt.Fprintf(b, "    (local.set $%s %s)\n", n, v)
		}
		e = lt.Body
	}
}

// lowerDraw emits the fixed 24-byte draw records at $base + i*24. Each record is
// [op, a, b, c, d, rgba]; geometry fills a..d, the Color fills rgba.
func lowerDraw(b *strings.Builder, d *Draw) error {
	for i, p := range d.Prims {
		op, ok := drawOpcode[p.Op]
		if !ok {
			return errf(p.Pos, "no opcode for draw prim %q", p.Op)
		}
		rec := i * 24
		// slots: op(0) a(4) b(8) c(12) d(16) rgba(20). op = (layer<<8)|primitive;
		// APP content draws on layer 1 (the widget/app canvas), not layer 0 (the
		// static basemap) — the runtime and acceptance filter app draws by layer 1.
		vals := make([]string, 6)
		vals[0] = fmt.Sprintf("(i32.const %d)", (appDrawLayer<<8)|op)
		vals[5] = "(i32.const 0)"
		// geometry args are all but the last (color); last is rgba.
		for gi := 0; gi < len(p.Args)-1; gi++ {
			s, err := lowerExpr(p.Args[gi])
			if err != nil {
				return err
			}
			vals[1+gi] = s
		}
		color, err := lowerExpr(p.Args[len(p.Args)-1])
		if err != nil {
			return err
		}
		vals[5] = color
		for slot, v := range vals {
			if v == "" {
				v = "(i32.const 0)"
			}
			fmt.Fprintf(b, "    (i32.store (i32.add (local.get $base) (i32.const %d)) %s)\n", rec+slot*4, v)
		}
	}
	return nil
}

func letNames(e Expr) []string {
	var out []string
	for {
		lt, ok := e.(*Let)
		if !ok {
			return out
		}
		out = append(out, lt.Names...)
		e = lt.Body
	}
}

// lowerExpr renders one expression as a folded WAT value form (i32).
func lowerExpr(e Expr) (string, error) {
	switch t := e.(type) {
	case *IntLit:
		return fmt.Sprintf("(i32.const %d)", t.V), nil
	case *ColorLit:
		return fmt.Sprintf("(i32.const 0x%X)", t.V), nil
	case *BoolLit:
		if t.V {
			return "(i32.const 1)", nil
		}
		return "(i32.const 0)", nil
	case *Var:
		return fmt.Sprintf("(local.get $%s)", t.Name), nil
	case *Prim:
		return lowerPrim(t)
	default:
		return "", errf(e.pos(), "cannot lower %T in value position", e)
	}
}

func lowerPrim(p *Prim) (string, error) {
	a := make([]string, len(p.Args))
	for i, arg := range p.Args {
		s, err := lowerExpr(arg)
		if err != nil {
			return "", err
		}
		a[i] = s
	}
	bin := func(op string) string { return fmt.Sprintf("(%s %s %s)", op, a[0], a[1]) }
	switch p.Op {
	case "+":
		return bin("i32.add"), nil
	case "-":
		return bin("i32.sub"), nil
	case "*":
		return bin("i32.mul"), nil
	case "/":
		return bin("i32.div_s"), nil
	case "mod":
		return bin("i32.rem_s"), nil
	case "<":
		return bin("i32.lt_s"), nil
	case "<=":
		return bin("i32.le_s"), nil
	case ">":
		return bin("i32.gt_s"), nil
	case ">=":
		return bin("i32.ge_s"), nil
	case "=":
		return bin("i32.eq"), nil
	case "!=":
		return bin("i32.ne"), nil
	case "and":
		return bin("i32.and"), nil
	case "or":
		return bin("i32.or"), nil
	case "not":
		return fmt.Sprintf("(i32.eqz %s)", a[0]), nil
	case "neg":
		return fmt.Sprintf("(i32.sub (i32.const 0) %s)", a[0]), nil
	case "abs":
		return fmt.Sprintf("(select %s (i32.sub (i32.const 0) %s) (i32.ge_s %s (i32.const 0)))", a[0], a[0], a[0]), nil
	case "min":
		return selectMin(a[0], a[1]), nil
	case "max":
		return selectMax(a[0], a[1]), nil
	case "clamp": // max(lo, min(x, hi))
		return selectMax(a[1], selectMin(a[0], a[2])), nil
	case "if": // (select then else cond)
		return fmt.Sprintf("(select %s %s %s)", a[1], a[2], a[0]), nil
	default:
		return "", errf(p.pos(), "no lowering for primitive %q", p.Op)
	}
}

// selectMin/selectMax render min/max via select (both branches are pure, so
// double-evaluation is semantically safe).
func selectMin(x, y string) string {
	return fmt.Sprintf("(select %s %s (i32.lt_s %s %s))", x, y, x, y)
}
func selectMax(x, y string) string {
	return fmt.Sprintf("(select %s %s (i32.gt_s %s %s))", x, y, x, y)
}

// drawOpcode maps a prim name to its primitive opcode (low byte). The 24-byte
// record is [op, a, b, c, d, rgba]; geometry fills a..d, color fills rgba.
var drawOpcode = map[string]int{"rect": 1, "line": 2, "circle": 3}

// appDrawLayer is the compositing layer app cells draw on (op high byte): 1 =
// widget/app canvas (0 is the static basemap, reserved for the host).
const appDrawLayer = 1

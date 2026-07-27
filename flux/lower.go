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
	// Distil to the core: inline the prologue derivations (neg/abs/min/max/clamp and
	// any evolved words) so only core operators reach the backend below. This is the
	// one place expansion happens; lowerPrim handles CORE ops only (prologue.go).
	p, err := currentPrologue()
	if err != nil {
		return "", err
	}
	if c, err = p.expand(c); err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("(module\n")
	b.WriteString("  (import \"hdm:kernel/hardware-io\" \"shared-cluster-memory\" (memory 100))\n")

	export := "run-tick"
	if c.Kind == KindView {
		export = "render-frame"
	}
	fmt.Fprintf(&b, "  (func (export %q) (param $base i32) (param $cap i32) (result i32)\n", export)

	// Locals: one per SCALAR read field and one per let binding (all i32), at the top.
	// A buffer read is not a scalar local — it is indexed directly at its base by
	// (at …)/(len …), so it is skipped here.
	var scalars []string
	for _, r := range c.Reads {
		if layout[r].Type != TBuffer {
			scalars = append(scalars, r)
		}
	}
	for _, n := range append(append([]string{}, scalars...), letNames(c.Body)...) {
		fmt.Fprintf(&b, "    (local $%s i32)\n", n)
	}

	// Load each scalar read field from its offset into its local.
	for _, r := range scalars {
		fmt.Fprintf(&b, "    (local.set $%s (i32.load (i32.const 0x%X)))\n", r, layout[r].Offset)
	}

	// Emit the outer lets, then the terminal write/draw + the return value.
	term, err := lowerLets(&b, c.Body, layout)
	if err != nil {
		return "", err
	}
	switch t := term.(type) {
	case *Write:
		for i, f := range t.Fields {
			v, err := lowerExpr(t.Vals[i], layout)
			if err != nil {
				return "", err
			}
			fmt.Fprintf(&b, "    (i32.store (i32.const 0x%X) %s)\n", layout[f].Offset, v)
		}
		for _, bs := range t.Stores {
			s, err := lowerBufStore(bs, layout)
			if err != nil {
				return "", err
			}
			b.WriteString(s)
		}
		b.WriteString("    (i32.const 0))\n)\n") // run-tick returns 0
	case *Draw:
		if err := lowerDraw(&b, t, layout); err != nil {
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
func lowerLets(b *strings.Builder, e Expr, layout Layout) (Expr, error) {
	for {
		lt, ok := e.(*Let)
		if !ok {
			return e, nil
		}
		for i, n := range lt.Names {
			v, err := lowerExpr(lt.Vals[i], layout)
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
func lowerDraw(b *strings.Builder, d *Draw, layout Layout) error {
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
			s, err := lowerExpr(p.Args[gi], layout)
			if err != nil {
				return err
			}
			vals[1+gi] = s
		}
		color, err := lowerExpr(p.Args[len(p.Args)-1], layout)
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
func lowerExpr(e Expr, layout Layout) (string, error) {
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
		return lowerPrim(t, layout)
	default:
		return "", errf(e.pos(), "cannot lower %T in value position", e)
	}
}

func lowerPrim(p *Prim, layout Layout) (string, error) {
	// Buffer primitives address a layout buffer directly (not a scalar local), so they
	// are lowered before the arg-lowering loop below (whose Var → local.get is wrong
	// for a buffer operand).
	switch p.Op {
	case "at":
		return lowerBufAt(p, layout)
	case "len":
		f, err := bufField(p.Args[0], layout)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("(i32.const %d)", f.Len), nil
	}
	a := make([]string, len(p.Args))
	for i, arg := range p.Args {
		s, err := lowerExpr(arg, layout)
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
	case "if": // (select then else cond)
		return fmt.Sprintf("(select %s %s %s)", a[1], a[2], a[0]), nil
	default:
		// neg/abs/min/max/clamp and any evolved word are DERIVATIONS — they must have
		// been inlined by the prologue expansion before lowering (prologue.go). Reaching
		// here means expansion was skipped or the prologue lacks the derivation.
		return "", errf(p.pos(), "no core lowering for %q (a derivation must be expanded via the prologue first)", p.Op)
	}
}

// bufField resolves a buffer operand: it must be a Var naming a TBuffer field in the
// layout (buffers are not values, so they cannot be let-bound or computed).
func bufField(e Expr, layout Layout) (Field, error) {
	v, ok := e.(*Var)
	if !ok {
		return Field{}, errf(e.pos(), "a buffer operand must be a buffer field name")
	}
	f, ok := layout[v.Name]
	if !ok || f.Type != TBuffer {
		return Field{}, errf(e.pos(), "%q is not a Buffer field", v.Name)
	}
	return f, nil
}

// lowerBufAt lowers (at buf idx) to a bounds-CLAMPED i32 load: the index is clamped
// to [0, len-1] via select (both branches pure), so the access can never leave the
// buffer's declared window — a buffer field is isolation-safe like a scalar field.
func lowerBufAt(p *Prim, layout Layout) (string, error) {
	f, err := bufField(p.Args[0], layout)
	if err != nil {
		return "", err
	}
	if f.Len == 0 {
		return "(i32.const 0)", nil // empty buffer: every read is 0
	}
	idx, err := lowerExpr(p.Args[1], layout)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("(i32.load %s)", bufAddr(f, idx)), nil
}

// clampedIndex renders idx clamped to [0, len-1] via select (both branches pure). The
// bound that makes buffer access isolation-safe.
func clampedIndex(idx string, length uint32) string {
	hi := int32(length) - 1
	lo := fmt.Sprintf("(select %s (i32.const %d) (i32.lt_s %s (i32.const %d)))", idx, hi, idx, hi) // min(idx,hi)
	return fmt.Sprintf("(select %s (i32.const 0) (i32.gt_s %s (i32.const 0)))", lo, lo)            // max(0,·)
}

// bufAddr renders the byte address of buf[idx]: base + clamp(idx)*4.
func bufAddr(f Field, idx string) string {
	return fmt.Sprintf("(i32.add (i32.const 0x%X) (i32.mul %s (i32.const 4)))", f.Offset, clampedIndex(idx, f.Len))
}

// lowerBufStore emits (store buf idx val) as a bounds-clamped i32.store statement.
func lowerBufStore(bs BufStore, layout Layout) (string, error) {
	f, ok := layout[bs.Buf]
	if !ok || f.Type != TBuffer {
		return "", errf(bs.Pos, "%q is not a Buffer field", bs.Buf)
	}
	if f.Len == 0 {
		return "", nil // empty buffer: store is a no-op
	}
	idx, err := lowerExpr(bs.Idx, layout)
	if err != nil {
		return "", err
	}
	val, err := lowerExpr(bs.Val, layout)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("    (i32.store %s %s)\n", bufAddr(f, idx), val), nil
}

// drawOpcode maps a prim name to its primitive opcode (low byte). The 24-byte
// record is [op, a, b, c, d, rgba]; geometry fills a..d, color fills rgba.
var drawOpcode = map[string]int{"rect": 1, "line": 2, "circle": 3}

// appDrawLayer is the compositing layer app cells draw on (op high byte): 1 =
// widget/app canvas (0 is the static basemap, reserved for the host).
const appDrawLayer = 1

package flux

import (
	"fmt"
	"strings"
)

// FORTH SURFACE: a concatenative (postfix, stack) surface over the same Cell IR — an
// alternative to the S-expression surface, to be A/B'd for LLM efficiency (see
// docs/flux-surface-ir.md, docs/language-evolution.md). A cell is a stream of words:
//
//	ball_x vel_x + =: nx            \ prologue: name an intermediate (keeps the stack shallow)
//	ball_y vel_y + =: ny
//	nx 0 screen_w 1 - clamp -> ball_x   \ write: store the top of stack to a field
//	nx 0 < nx screen_w >= or  vel_x neg  vel_x  ? -> vel_x
//
// Words: a field or local name PUSHES its value; a literal pushes itself; an operator
// pops its args and pushes the result; `=: name` pops and binds a local (the prologue,
// keeping each stack 2–3 deep and locally inspectable); `-> field` pops and stores;
// `?` is if/select (cond then else -- v; both branches are pure). Reads/writes are
// DERIVED (the checker infers them), so no clauses appear — maximum token economy.
//
// Read TRANSPILES Forth to the terse S-expression form and reuses Parse+Check, so
// all typing/validation is shared and a Forth cell lands on the identical IR. Render
// is the inverse. Behavior is therefore guaranteed equal to the S-expr surface by
// BehaviorHash.
type Forth struct{}

func (Forth) Name() string { return "forth" }

func (Forth) Read(filename, src string, layout Layout) (*Cell, error) {
	sx, err := forthToSexpr(src, layout)
	if err != nil {
		return nil, err
	}
	return SExpr{}.Read(filename, sx, layout)
}

func (Forth) Render(c *Cell) string { return cellToForth(c) }

// ForthDiagnose validates a Forth cell and returns "ok" or a precise, actionable
// message naming what broke — a stack/word error (underflow, unknown word, dangling
// value) from the transpile, or a type/field error from the checker. It is the
// payload of the agentic validation tool: the model calls it, reads the feedback, and
// fixes the cell before committing (the same trick as flux_check for s-expr).
func ForthDiagnose(src string, layout Layout) string {
	sx, err := forthToSexpr(src, layout)
	if err != nil {
		return "REJECTED (stack/word): " + err.Error()
	}
	if _, err := (SExpr{}).Read("m", sx, layout); err != nil {
		return "REJECTED (type): " + err.Error()
	}
	return "ok"
}

// forth operator classes. Binops render/read as a 2-arg prefix form; unops 1-arg;
// clamp is 3-arg; `?` is the ternary select (if).
var forthBinop = map[string]bool{
	"+": true, "-": true, "*": true, "/": true, "mod": true,
	"<": true, "<=": true, ">": true, ">=": true, "=": true, "!=": true,
	"and": true, "or": true, "min": true, "max": true,
}
var forthUnop = map[string]bool{"neg": true, "abs": true, "not": true}

// forthDrawArity is the operand count each draw word pops (geometry + trailing color).
var forthDrawArity = map[string]int{"circle": 4, "rect": 5, "line": 5}

// forthToSexpr runs the stack machine over the word stream and emits the equivalent
// terse S-expression cell. Any malformed stream (underflow, unknown word, dangling
// values, no terminal) is a positioned-ish error — which the scoreboard counts as an
// invalid generation.
func forthToSexpr(src string, layout Layout) (string, error) {
	toks := strings.Fields(src)
	var stack []string
	var lets [][2]string   // ordered (localName, exprSexpr)
	var writes [][2]string // ordered (field, exprSexpr)
	var draws []string      // rendered draw prims
	locals := map[string]bool{}

	pop := func() (string, error) {
		if len(stack) == 0 {
			return "", fmt.Errorf("forth: stack underflow")
		}
		v := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		return v, nil
	}
	push := func(s string) { stack = append(stack, s) }

	for i := 0; i < len(toks); i++ {
		t := toks[i]
		switch {
		case t == "=:":
			i++
			if i >= len(toks) {
				return "", fmt.Errorf("forth: =: needs a local name")
			}
			e, err := pop()
			if err != nil {
				return "", err
			}
			lets = append(lets, [2]string{toks[i], e})
			locals[toks[i]] = true
		case t == "->":
			i++
			if i >= len(toks) {
				return "", fmt.Errorf("forth: -> needs a field name")
			}
			e, err := pop()
			if err != nil {
				return "", err
			}
			writes = append(writes, [2]string{toks[i], e})
		case t == "?":
			el, err1 := pop()
			th, err2 := pop()
			c, err3 := pop()
			if err1 != nil || err2 != nil || err3 != nil {
				return "", fmt.Errorf("forth: ? needs cond then else")
			}
			push("(if " + c + " " + th + " " + el + ")")
		case t == "clamp":
			hi, err1 := pop()
			lo, err2 := pop()
			x, err3 := pop()
			if err1 != nil || err2 != nil || err3 != nil {
				return "", fmt.Errorf("forth: clamp needs x lo hi")
			}
			push("(clamp " + x + " " + lo + " " + hi + ")")
		case forthBinop[t]:
			b, err1 := pop()
			a, err2 := pop()
			if err1 != nil || err2 != nil {
				return "", fmt.Errorf("forth: %q needs two operands", t)
			}
			push("(" + t + " " + a + " " + b + ")")
		case forthUnop[t]:
			a, err := pop()
			if err != nil {
				return "", err
			}
			push("(" + t + " " + a + ")")
		case forthDrawArity[t] > 0:
			n := forthDrawArity[t]
			args := make([]string, n)
			for k := n - 1; k >= 0; k-- {
				a, err := pop()
				if err != nil {
					return "", err
				}
				args[k] = a
			}
			draws = append(draws, "("+t+" "+strings.Join(args, " ")+")")
		case isForthLiteral(t):
			push(t)
		case layout[t].Type != TInvalid || locals[t]:
			push(t) // a field read or a bound local
		default:
			return "", fmt.Errorf("forth: unknown word %q", t)
		}
	}
	if len(stack) != 0 {
		return "", fmt.Errorf("forth: %d dangling value(s) — every value must be bound (=:), written (->), or drawn", len(stack))
	}

	var term string
	switch {
	case len(writes) > 0:
		parts := make([]string, len(writes))
		for i, w := range writes {
			parts[i] = "(" + w[0] + " " + w[1] + ")"
		}
		term = "(write " + strings.Join(parts, " ") + ")"
	case len(draws) > 0:
		term = "(draw " + strings.Join(draws, " ") + ")"
	default:
		return "", fmt.Errorf("forth: cell has no terminal (no -> write or draw word)")
	}
	if len(lets) > 0 {
		parts := make([]string, len(lets))
		for i, l := range lets {
			parts[i] = "[" + l[0] + " " + l[1] + "]"
		}
		term = "(let (" + strings.Join(parts, " ") + ") " + term + ")"
	}
	return "(cell c " + term + ")", nil
}

// isForthLiteral reports whether a token is an int / color / bool literal.
func isForthLiteral(t string) bool {
	switch t {
	case "true", "false":
		return true
	}
	if strings.HasPrefix(t, "#x") {
		return true
	}
	// signed integer
	s := t
	if strings.HasPrefix(s, "-") {
		s = s[1:]
	}
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// cellToForth renders a Cell as a Forth word stream: the lets become the prologue of
// `=: name` bindings, the terminal write/draw becomes `-> field` / draw words.
func cellToForth(c *Cell) string {
	if c == nil {
		return ""
	}
	var out []string
	e := c.Body
	for {
		lt, ok := e.(*Let)
		if !ok {
			break
		}
		for i, n := range lt.Names {
			out = append(out, rpn(lt.Vals[i]), "=:", n)
		}
		e = lt.Body
	}
	switch t := e.(type) {
	case *Write:
		for i, f := range t.Fields {
			out = append(out, rpn(t.Vals[i]), "->", f)
		}
	case *Draw:
		for _, p := range t.Prims {
			for _, a := range p.Args {
				out = append(out, rpn(a))
			}
			out = append(out, p.Op)
		}
	}
	return strings.Join(out, " ")
}

// rpn renders one expression in postfix (the inverse of the stack machine).
func rpn(e Expr) string {
	switch x := e.(type) {
	case *IntLit:
		return fmt.Sprintf("%d", x.V)
	case *FloatLit:
		s := fmt.Sprintf("%g", x.V)
		if !strings.ContainsRune(s, '.') {
			s += ".0"
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
		parts := make([]string, 0, len(x.Args)+1)
		for _, a := range x.Args {
			parts = append(parts, rpn(a))
		}
		parts = append(parts, forthWord(x.Op))
		return strings.Join(parts, " ")
	default:
		return ""
	}
}

// forthWord maps an IR op to its Forth word: `if` prints as the ternary `?`, all
// others keep their name.
func forthWord(op string) string {
	if op == "if" {
		return "?"
	}
	return op
}

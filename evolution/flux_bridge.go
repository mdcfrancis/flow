package evolution

import (
	"fmt"
	"sort"
	"strings"

	"github.com/mdcfrancis/flow/flux"
)

// This file bridges the Flux functional IR (docs/functional-ir.md) into the
// operational synthesis path. When a layout is available, the sieve accepts a
// model that authors a Flux (cell …) program: it is parsed, type-checked, and
// lowered to WAT here, then verified by the identical downstream gates. A model
// that still emits raw WAT is unaffected — extractFlux returns "" and the WAT
// path runs exactly as before.

// LayoutFromContract turns the app's shared-state contract into a Flux field
// Layout (name → type + offset). Only the scalar types Flux v1 lowers are
// included; array/unknown fields are omitted (a Flux cell that reads one fails
// type-checking with a clear message, rather than lowering to bad addressing).
// Returns nil when nothing is addressable, which disables the Flux path.
func LayoutFromContract(c *AppContract) flux.Layout {
	if c == nil {
		return nil
	}
	l := flux.Layout{}
	for _, f := range c.Fields {
		var t flux.Type
		switch strings.TrimSpace(f.Type) {
		case "i32":
			t = flux.TInt
		case "f32":
			t = flux.TFloat
		default:
			continue // arrays / unknown: not addressable by Flux v1
		}
		l[f.Name] = flux.Field{Type: t, Offset: uint32(f.Offset)}
	}
	if len(l) == 0 {
		return nil
	}
	return l
}

// fluxLayoutFor returns the Flux field layout for a cell's app when the Flux path
// is enabled and the app has an addressable contract, or nil (which disables the
// Flux path for that cell — it is synthesized as WAT, unchanged).
func (o *Orchestrator) fluxLayoutFor(urn string) flux.Layout {
	if !o.FluxEnabled {
		return nil
	}
	return LayoutFromContract(LoadContract(o.ledger, AppNamespaceOf(urn)))
}

// candidateWAT converts one model response into WAT for the sieve to assemble.
// With a layout and a Flux (cell …) form present, it lowers Flux → WAT and marks
// the source Flux; otherwise it extracts WAT as before. A Flux compile error is
// returned so the loop can feed the semantic message back for repair.
func candidateWAT(resp string, layout flux.Layout) (wat, fluxSrc string, err error) {
	if layout != nil {
		if src := extractFlux(resp); src != "" {
			w, cerr := flux.Compile("cell", src, layout)
			if cerr != nil {
				return "", src, cerr
			}
			return w, src, nil
		}
	}
	return extractWAT(resp), "", nil
}

// extractFlux isolates the outermost balanced (cell …) form from a completion,
// tolerating markdown fences and surrounding prose. Returns "" if none.
func extractFlux(resp string) string {
	s := resp
	if i := strings.Index(s, "```"); i >= 0 {
		rest := s[i+3:]
		if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
			rest = rest[nl+1:]
		}
		if j := strings.Index(rest, "```"); j >= 0 {
			s = rest[:j]
		} else {
			s = rest
		}
	}
	start := strings.Index(s, "(cell")
	if start < 0 {
		return ""
	}
	depth, inStr := 0, false
	for i := start; i < len(s); i++ {
		c := s[i]
		switch {
		case inStr:
			if c == '"' {
				inStr = false
			}
		case c == '"':
			inStr = true
		case c == '(':
			depth++
		case c == ')':
			depth--
			if depth == 0 {
				return s[start : i+1]
			}
		}
	}
	return ""
}

// fluxSeedBlock renders the Flux authoring instructions appended to the build
// seed when the Flux path is on: it tells the model to output a (cell …) program
// (not WAT), gives the grammar + the typed field list + a worked example matching
// the cell's entry, so the model writes only logic and the lowerer owns the
// encoding.
func fluxSeedBlock(contract *EntryContract, layout flux.Layout) string {
	view := contract != nil && contract.Name == "render-frame"
	fields := make([]string, 0, len(layout))
	for n, f := range layout {
		fields = append(fields, fmt.Sprintf("%s : %s", n, f.Type))
	}
	sort.Strings(fields)

	var b strings.Builder
	b.WriteString("\n=== OUTPUT FORMAT: FLUX (author logic, NOT WAT) ===\n")
	b.WriteString("Write this cell as a Flux functional program — a typed S-expression that is\n")
	b.WriteString("compiled to WASM for you. Do NOT write WAT, WASM, or (module …). Output ONLY a (cell …) form.\n\n")
	b.WriteString("A cell is a PURE FUNCTION over shared state; the compiler owns all memory, stack, and types:\n")
	if view {
		b.WriteString("  (cell NAME (reads <fields>) (draw <prims>))\n")
		b.WriteString("  prims: (circle cx cy r color) (rect x y w h color) (line x1 y1 x2 y2 color); color is #xRRGGBBAA.\n")
	} else {
		b.WriteString("  (cell NAME (reads <fields>) (writes <fields>) BODY)\n")
		b.WriteString("  BODY = optional (let ([name expr]…) …) ending in (write (field expr) …). Unwritten fields keep their value.\n")
	}
	b.WriteString("Expressions: Int/Float/Bool/Color literals (42, 3.14, true, #xFF8800FF); field & let names;\n")
	b.WriteString("(if cond then else); primitives  + - * / mod neg abs min max clamp  < <= > >= = != and or not.\n")
	b.WriteString("Comparisons yield Bool; there is no implicit numeric coercion.\n\n")
	fmt.Fprintf(&b, "AVAILABLE SHARED FIELDS (name : type) — read/write only within your enforced boundary above:\n  %s\n\n", strings.Join(fields, ", "))
	if view {
		b.WriteString("WORKED EXAMPLE (a view cell that draws AT its read position):\n")
		b.WriteString("  (cell renderer (reads ball_x ball_y) (draw (circle ball_x ball_y 8 #xFFCC33FF)))\n\n")
	} else {
		b.WriteString("WORKED EXAMPLE (a physics cell: integrate, reflect at the walls, clamp):\n")
		b.WriteString("  (cell physics\n")
		b.WriteString("    (reads ball_x ball_y vel_x vel_y screen_w screen_h)\n")
		b.WriteString("    (writes ball_x ball_y vel_x vel_y)\n")
		b.WriteString("    (let ([nx (+ ball_x vel_x)] [ny (+ ball_y vel_y)]\n")
		b.WriteString("          [bx (or (< nx 0) (>= nx screen_w))] [by (or (< ny 0) (>= ny screen_h))])\n")
		b.WriteString("      (write (vel_x (if bx (neg vel_x) vel_x)) (vel_y (if by (neg vel_y) vel_y))\n")
		b.WriteString("             (ball_x (clamp nx 0 (- screen_w 1))) (ball_y (clamp ny 0 (- screen_h 1))))))\n\n")
	}
	b.WriteString("Output only your (cell …) program.\n")
	return b.String()
}

// fluxCorrectionDirective renders the sieve repair prompt for a Flux compile
// error — a semantic sentence (a type/shape/parse message), not a stack trace.
func fluxCorrectionDirective(err error) string {
	return fmt.Sprintf(`[INNER SIEVE CORRECTION DIRECTIVE]
Your Flux program did not compile.
DEFECT: %v
Regenerate the complete (cell …) program, fixing exactly that. Output only the Flux program.`, err)
}

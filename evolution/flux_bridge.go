package evolution

import (
	"fmt"
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

// fluxCorrectionDirective renders the sieve repair prompt for a Flux compile
// error — a semantic sentence (a type/shape/parse message), not a stack trace.
func fluxCorrectionDirective(err error) string {
	return fmt.Sprintf(`[INNER SIEVE CORRECTION DIRECTIVE]
Your Flux program did not compile.
DEFECT: %v
Regenerate the complete (cell …) program, fixing exactly that. Output only the Flux program.`, err)
}

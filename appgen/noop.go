package appgen

import (
	"fmt"
	"sort"
	"strings"

	"github.com/mdcfrancis/flow/evolution"
	"github.com/mdcfrancis/flow/flux"
)

// positionArrays finds an x/y POSITION array pair in a layout (e.g. particle_x/particle_y,
// both i32[N]) and a loop bound for it, so an array-backed render cell can be scaffolded
// as a loop over the field. It pairs an array whose name ends in x with its y sibling
// (…_x/…_y, or …x/…y) — excluding velocity arrays (…vx/…vy) — and picks the loop count
// from a scalar count/num field if present, else the array's declared length. Deterministic
// (names are sorted). ok=false when there is no such pair.
func positionArrays(fields flux.Layout) (xf, yf, count string, ok bool) {
	isArr := func(n string) bool { f, o := fields[n]; return o && f.Type == flux.TBuffer }
	names := make([]string, 0, len(fields))
	for n := range fields {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if !isArr(n) || strings.HasSuffix(n, "vx") {
			continue
		}
		var y string
		switch {
		case strings.HasSuffix(n, "_x"):
			y = strings.TrimSuffix(n, "_x") + "_y"
		case strings.HasSuffix(n, "x"):
			y = strings.TrimSuffix(n, "x") + "y"
		default:
			continue
		}
		if isArr(y) {
			xf, yf, ok = n, y, true
			break
		}
	}
	if !ok {
		return
	}
	for _, n := range names {
		if f, o := fields[n]; o && f.Type != flux.TBuffer {
			ln := strings.ToLower(n)
			if strings.Contains(ln, "count") || strings.Contains(ln, "num") || ln == "n" {
				count = "(get " + n + ")"
				break
			}
		}
	}
	if count == "" {
		count = fmt.Sprintf("(i32.const %d)", fields[xf].Len)
	}
	return
}

// macroFieldsFromContract builds the macro field Layout (name → type + offset) from the
// app contract — the same ground truth the synthesis path expands against.
func macroFieldsFromContract(c *evolution.AppContract) flux.Layout {
	m := flux.Layout{}
	if c == nil {
		return m
	}
	for _, f := range c.Fields {
		t := flux.TInt
		if strings.TrimSpace(f.Type) == "f32" {
			t = flux.TFloat
		}
		m[f.Name] = flux.Field{Type: t, Offset: uint32(f.Offset)}
	}
	return m
}

// macroNoop builds a no-op MACRO-WAT genome: a compute cell writes each addressable
// write-port back unchanged (scalar via (set/get), array via (setidx/atidx element 0));
// a render cell draws its natural shape — a LOOP over the position arrays if the app has
// them (an array-backed visual), else a magenta checkerboard placeholder. Returns
// ok=false if there is nothing addressable to write (caller keeps the WAT skeleton).
func macroNoop(sub Subsystem, fields flux.Layout, arrays map[string]bool) (string, bool) {
	if kindOf(sub) == KindRender {
		// If the contract has an x/y position ARRAY pair, the render is array-backed:
		// scaffold the ITERATION SHAPE — a circle per element — so the cell is born with a
		// loop over the field to refine, not a static checkerboard it must discard. This is
		// the render analogue of the compute no-op writing each port back.
		if xf, yf, count, ok := positionArrays(fields); ok {
			// A per-element varied colour so the field is many-hued (nudges visual_coverage).
			color := "(i32.or (i32.shl (i32.and (i32.mul (local.get $i) (i32.const 20)) (i32.const 255)) (i32.const 24)) (i32.const 0x40E0FF))"
			return fmt.Sprintf("(cell render-frame (for $i %s (draw (circle (atidx %s $i) (atidx %s $i) (i32.const 3) %s))))", count, xf, yf, color), true
		}
		const cw, ch, tile = 320, 240, 80
		var prims strings.Builder
		for y := 0; y < ch; y += tile {
			for x := 0; x < cw; x += tile {
				if ((x/tile)+(y/tile))%2 == 0 {
					fmt.Fprintf(&prims, " (rect (i32.const %d) (i32.const %d) (i32.const %d) (i32.const %d) (i32.const 0xFF00FFFF))", x, y, tile, tile)
				}
			}
		}
		return fmt.Sprintf("(cell render-frame (scene%s))", prims.String()), true
	}

	var body strings.Builder
	for _, w := range sub.Writes {
		if _, ok := fields[w]; !ok {
			continue
		}
		if arrays[w] {
			fmt.Fprintf(&body, " (setidx %s (i32.const 0) (atidx %s (i32.const 0)))", w, w)
		} else {
			fmt.Fprintf(&body, " (set %s (get %s))", w, w)
		}
	}
	if body.Len() == 0 {
		return "", false
	}
	return fmt.Sprintf("(cell run-tick%s (i32.const 0))", body.String()), true
}

// seedNoopMacro returns the no-op macro-WAT genome + its assembled bytecode, or ok=false
// if there is no Flux-addressable contract or it does not assemble (genesis keeps WAT).
func (g *Grower) seedNoopMacro(env *AppEnvelope, sub Subsystem) (src string, bc []byte, ok bool) {
	contract := evolution.LoadContract(g.ledger, env.ApplicationNamespace)
	if contract == nil || len(contract.Fields) == 0 {
		return "", nil, false
	}
	fields := macroFieldsFromContract(contract)
	arrays := map[string]bool{}
	for _, f := range contract.Fields {
		if strings.Contains(f.Type, "[") {
			arrays[f.Name] = true
		}
	}
	m, ok := macroNoop(sub, fields, arrays)
	if !ok {
		return "", nil, false
	}
	wat, err := flux.Expand(m, fields)
	if err != nil {
		return "", nil, false
	}
	art, err := g.sieve.CompileGenotype(wat)
	if err != nil || art == nil || !art.SyntaxPassed {
		return "", nil, false
	}
	return m, art.Bytecode, true
}


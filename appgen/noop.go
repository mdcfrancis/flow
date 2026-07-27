package appgen

import (
	"fmt"
	"strings"

	"github.com/mdcfrancis/flow/evolution"
	"github.com/mdcfrancis/flow/flux"
)

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
// a render cell draws a magenta checkerboard placeholder. Returns ok=false if there is
// nothing addressable to write (caller keeps the WAT skeleton).
func macroNoop(sub Subsystem, fields flux.Layout, arrays map[string]bool) (string, bool) {
	if kindOf(sub) == KindRender {
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


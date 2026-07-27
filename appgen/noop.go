package appgen

import (
	"fmt"
	"strings"

	"github.com/mdcfrancis/flow/evolution"
	"github.com/mdcfrancis/flow/flux"
)

// noopFlux builds a minimal, valid no-op Flux cell for a freshly-scaffolded
// subsystem. In Flux mode this becomes the cell's initial genome, so the stored
// artifact is Flux FROM BIRTH and the synthesis loop iterates on a real Flux draft
// ("improve THIS, do not restart") rather than building from scratch on top of a
// WAT skeleton it cannot extend.
//
//   - compute: writes each addressable write-port back UNCHANGED — a true no-op
//     that still establishes the cell's read/write shape for the model to build on.
//   - view: draws a fixed CHECKERBOARD test pattern from constants (no state
//     reads) — the universal "placeholder / missing content" marker. Deliberately
//     NOT the real sprite: a seed that drew (circle ball_x ball_y …) would already
//     BE the solution for a ball app, so the model would build nothing and the
//     position scenarios would pass by luck. A checkerboard is visibly a scaffold —
//     it satisfies "renders ≥1 primitive" so the canvas isn't blank, but every
//     position scenario stays failing until the model replaces it with the real
//     state-driven draw.
//
// Returns ok=false when the ports aren't addressable by Flux (no i32/f32 contract
// layout for them) — the caller then keeps the WAT skeleton for that cell.
func noopFlux(sub Subsystem, layout flux.Layout) (src string, ok bool) {
	name := sub.Identity
	if i := strings.LastIndexByte(name, ':'); i >= 0 {
		name = name[i+1:]
	}
	addressable := func(f string) bool { _, in := layout[f]; return in }

	if kindOf(sub) == KindRender {
		// Magenta tiles on the (black) canvas, one rect per filled square of a
		// coarse checkerboard. Constants only — clearly a test pattern, not any
		// app's real output.
		const cw, ch, tile = 320, 240, 80
		var tiles strings.Builder
		for y := 0; y < ch; y += tile {
			for x := 0; x < cw; x += tile {
				if ((x/tile)+(y/tile))%2 == 0 {
					fmt.Fprintf(&tiles, " (rect %d %d %d %d #xFF00FFFF)", x, y, tile, tile)
				}
			}
		}
		return fmt.Sprintf("(cell %s (reads) (draw%s))", name, tiles.String()), true
	}

	// Compute: write every addressable write-port back to itself. reads == writes so
	// each written field is in scope as a read var.
	var writes []string
	for _, w := range sub.Writes {
		if addressable(w) {
			writes = append(writes, w)
		}
	}
	if len(writes) == 0 {
		return "", false // nothing addressable to write — fall back to WAT
	}
	var pairs strings.Builder
	for _, w := range writes {
		fmt.Fprintf(&pairs, " (%s %s)", w, w)
	}
	list := strings.Join(writes, " ")
	return fmt.Sprintf("(cell %s (reads %s) (writes %s) (write%s))",
		name, list, list, pairs.String()), true
}

// seedNoopFlux returns the no-op Flux genome + its compiled bytecode for a
// subsystem, or ok=false if the app has no Flux-addressable layout or the no-op
// does not compile (so genesis keeps the WAT path). The layout comes from the
// app's persisted contract — the same ground truth the orchestrator's Flux path
// uses — so the seed's field offsets match what synthesis will build against.
func (g *Grower) seedNoopFlux(env *AppEnvelope, sub Subsystem) (src string, bc []byte, ok bool) {
	contract := evolution.LoadContract(g.ledger, env.ApplicationNamespace)
	layout := evolution.LayoutFromContract(contract)
	if layout == nil {
		return "", nil, false
	}
	sexpr, ok := noopFlux(sub, layout)
	if !ok {
		return "", nil, false
	}
	// Bytecode from the canonical s-expr (the Go backend is surface-invariant).
	wat, err := flux.Compile("cell", sexpr, layout)
	if err != nil {
		return "", nil, false
	}
	art, err := g.sieve.CompileGenotype(wat)
	if err != nil || art == nil || !art.SyntaxPassed {
		return "", nil, false
	}
	// STORE the genome in the ACTIVE surface (Forth by default), so the seed is in the
	// same surface the model is asked to author in — the synthesis loop then iterates on
	// a same-surface draft instead of being shown s-expr while told to write Forth. The
	// backend is identical either way; only the stored text changes. Fall back to the
	// s-expr text if the round-trip render fails.
	src = sexpr
	if cell, rerr := (flux.SExpr{}).Read("seed", sexpr, layout); rerr == nil {
		if rendered := evolution.ActiveSurface().Render(cell); strings.TrimSpace(rendered) != "" {
			src = rendered
		}
	}
	return src, art.Bytecode, true
}

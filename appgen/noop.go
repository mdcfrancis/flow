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
//   - view: draws a placeholder circle at its first two addressable read-ports
//     (falling back to screen-centre constants), so the canvas shows something and
//     the renderer has a working draw call to refine.
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
		var reads []string
		for _, r := range sub.Reads {
			if addressable(r) {
				reads = append(reads, r)
			}
		}
		cx, cy := "160", "120" // screen-ish centre when no positional reads exist
		if len(reads) >= 2 {
			cx, cy = reads[0], reads[1]
		}
		// A visible white placeholder circle — the seed the renderer refines to
		// track real state.
		return fmt.Sprintf("(cell %s (reads %s) (draw (circle %s %s 6 #xFFFFFFFF)))",
			name, strings.Join(reads, " "), cx, cy), true
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
	src, ok = noopFlux(sub, layout)
	if !ok {
		return "", nil, false
	}
	wat, err := flux.Compile("cell", src, layout)
	if err != nil {
		return "", nil, false
	}
	art, err := g.sieve.CompileGenotype(wat)
	if err != nil || art == nil || !art.SyntaxPassed {
		return "", nil, false
	}
	return src, art.Bytecode, true
}

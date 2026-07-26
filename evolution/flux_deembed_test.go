package evolution

import (
	"strings"
	"testing"

	"github.com/mdcfrancis/flow/flux"
)

// The de-embedding invariant (docs/language-evolution.md §6, docs/lineage.md §8):
// fluxSeedBlock describes the LANGUAGE (grammar + fields) but embeds no worked Flux
// PROGRAM — the worked example is single-sourced in the KB and lazily inlined.
func TestFluxSeedBlockEmbedsNoFluxProgram(t *testing.T) {
	layout := flux.Layout{
		"ball_x": {Type: flux.TInt, Offset: 0xB0000},
		"ball_y": {Type: flux.TInt, Offset: 0xB0004},
	}
	// The block may describe the grammar schema "(cell NAME …)", but must not embed a
	// concrete worked PROGRAM — the signatures below are the ones that used to be
	// hard-coded and now live only in the KB.
	banned := []string{"(cell renderer", "(cell physics", "(draw (circle ball_x", "(clamp nx"}
	for _, contract := range []*EntryContract{RunTickContract, RenderFrameContract} {
		block := fluxSeedBlock(contract, layout)
		for _, b := range banned {
			if strings.Contains(block, b) {
				t.Fatalf("fluxSeedBlock must embed no worked Flux program, but contains %q:\n%s", b, block)
			}
		}
	}
}

// Seeding puts the worked Flux examples in the store, retrievable under lang="flux";
// the WAT seed example is NOT returned for a Flux authoring, and vice versa.
func TestSeededFluxExamplesAreRetrievableByLang(t *testing.T) {
	le := newLedger(t)
	SeedKnowledge(le)

	fx := FindExamples(le, "render", "flux", "draw the ball at its position", []string{"ball_x", "ball_y"}, nil, 5)
	if len(fx) == 0 {
		t.Fatal("expected a seeded Flux render example")
	}
	for _, e := range fx {
		if langOf(e) != "flux" {
			t.Fatalf("lang=flux filter leaked a %s example: %q", langOf(e), e.Semantics)
		}
		if !strings.Contains(e.WAT, "(cell ") {
			t.Fatalf("a flux example's source should be a (cell …) program, got: %q", e.WAT)
		}
	}

	// The Flux physics example must be retrievable for a compute cell.
	phys := FindExamples(le, "compute", "flux", "integrate and bounce off the walls", nil, nil, 5)
	if len(phys) == 0 || !strings.Contains(phys[0].WAT, "clamp") {
		t.Fatalf("expected the seeded Flux wall-bounce example first, got %+v", phys)
	}

	// A WAT authoring must NOT be shown the Flux examples.
	wat := FindExamples(le, "render", "wat", "draw the ball at its position", nil, nil, 5)
	for _, e := range wat {
		if langOf(e) == "flux" {
			t.Fatalf("lang=wat retrieval leaked a Flux example: %q", e.Semantics)
		}
	}
}

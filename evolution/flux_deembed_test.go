package evolution

import (
	"strings"
	"testing"

	"github.com/mdcfrancis/flow/flux"
)

// The de-embedding invariant: macroSeedBlock describes the SURFACE (the macro forms +
// fields) but embeds no worked PROGRAM — the worked example is single-sourced in the
// KB and lazily inlined by renderKnowledge.
func TestMacroSeedBlockEmbedsNoProgram(t *testing.T) {
	layout := flux.Layout{
		"ball_x": {Type: flux.TInt, Offset: 0xB0000},
		"ball_y": {Type: flux.TInt, Offset: 0xB0004},
	}
	// The block may describe the macro forms "(cell …)"/"(get NAME)", but must not embed
	// a concrete worked PROGRAM — the signatures below are the ones that live only in KB.
	banned := []string{"(scene (circle (get ball_x)", "(local.set $nx", "(set vel_x"}
	for _, contract := range []*EntryContract{RunTickContract, RenderFrameContract} {
		block := macroSeedBlock(contract, layout)
		for _, b := range banned {
			if strings.Contains(block, b) {
				t.Fatalf("macroSeedBlock must embed no worked program, but contains %q:\n%s", b, block)
			}
		}
	}
}

// Seeding puts the worked macro-WAT examples in the store, retrievable under
// lang="flux"; the raw-WAT seed example is NOT returned for a macro authoring, and
// vice versa.
func TestSeededMacroExamplesAreRetrievableByLang(t *testing.T) {
	le := newLedger(t)
	SeedKnowledge(le)

	fx := FindExamples(le, "render", "flux", "draw the ball at its position", []string{"ball_x", "ball_y"}, nil, 5)
	if len(fx) == 0 {
		t.Fatal("expected a seeded macro render example")
	}
	for _, e := range fx {
		if langOf(e) != "flux" {
			t.Fatalf("lang=flux filter leaked a %s example: %q", langOf(e), e.Semantics)
		}
		if !strings.Contains(e.WAT, "(cell ") {
			t.Fatalf("a macro example's source should be a (cell …) program, got: %q", e.WAT)
		}
	}

	// The macro wall-bounce example must be retrievable for a compute cell.
	phys := FindExamples(le, "compute", "flux", "integrate and bounce off the walls", nil, nil, 5)
	if len(phys) == 0 || !strings.Contains(phys[0].WAT, "vel_x") {
		t.Fatalf("expected the seeded macro wall-bounce example first, got %+v", phys)
	}

	// A raw-WAT authoring must NOT be shown the macro examples.
	wat := FindExamples(le, "render", "wat", "draw the ball at its position", nil, nil, 5)
	for _, e := range wat {
		if langOf(e) == "flux" {
			t.Fatalf("lang=wat retrieval leaked a macro example: %q", e.Semantics)
		}
	}
}

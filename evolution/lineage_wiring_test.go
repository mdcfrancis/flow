package evolution

import (
	"testing"

	"github.com/mdcfrancis/flow/flux"
	"github.com/mdcfrancis/flow/manifest"
)

// authoringLineage records the input edges knowable at build time, and distinguishes
// a Flux authoring (language version + grammar hash) from a WAT one.
func TestAuthoringLineageEdges(t *testing.T) {
	le := newLedger(t)
	o := &Orchestrator{ledger: le, CompassPrompt: "SYSTEM PROMPT", FluxEnabled: true}
	layout := flux.Layout{
		"ball_x": {Type: flux.TInt, Offset: 0xB0000},
		"ball_y": {Type: flux.TInt, Offset: 0xB0004},
	}

	lf := o.authoringLineage("urn:hdm:app:demo:view", "urn:hdm:app:demo", RenderFrameContract, nil, layout, true, []string{"ex1"})
	if lf.Language != FluxLanguageVersion {
		t.Fatalf("flux cell language = %q, want %q", lf.Language, FluxLanguageVersion)
	}
	if lf.Grammar == "" {
		t.Fatal("flux cell must record the grammar (GBNF) hash it decodes under")
	}
	if lf.Prompt == "" {
		t.Fatal("must record the prompt-template hash")
	}
	if len(lf.Examples) != 1 || lf.Examples[0] != "ex1" {
		t.Fatalf("examples edge lost: %+v", lf.Examples)
	}

	lw := o.authoringLineage("urn:hdm:app:demo:c", "urn:hdm:app:demo", RunTickContract, nil, nil, false, nil)
	if lw.Language != langWAT {
		t.Fatalf("wat cell language = %q, want %q", lw.Language, langWAT)
	}
	if lw.Grammar != "" {
		t.Fatal("a WAT cell has no grammar edge")
	}

	// A compute-kind and a view-kind Flux cell decode under different grammars, so
	// their grammar edges differ.
	lc := o.authoringLineage("urn:hdm:app:demo:phys", "urn:hdm:app:demo", RunTickContract, nil, layout, true, nil)
	if lc.Grammar == lf.Grammar {
		t.Fatal("compute and view cells must record distinct grammar hashes")
	}
}

// recordLineage attaches the committed genotype hash to the stashed inputs and
// persists it; a commit with no stashed inputs is not recorded (would pollute the
// memo with empty inputs).
func TestRecordLineageFromStash(t *testing.T) {
	le := newLedger(t)
	o := &Orchestrator{ledger: le}
	o.noteAuthoring("urn:x", Lineage{Language: FluxLanguageVersion, Examples: []string{"ex1"}})

	o.recordLineage("urn:x", &manifest.NodeDescriptor{GenotypeHash: "g1"})
	got, ok := LineageOf(le, "g1")
	if !ok || got.URN != "urn:x" || got.Language != FluxLanguageVersion || len(got.Examples) != 1 {
		t.Fatalf("stash was not recorded against the committed genotype: %+v ok=%v", got, ok)
	}

	o.recordLineage("urn:absent", &manifest.NodeDescriptor{GenotypeHash: "g2"})
	if _, ok := LineageOf(le, "g2"); ok {
		t.Fatal("a commit with no stashed authoring inputs must not record lineage")
	}
}

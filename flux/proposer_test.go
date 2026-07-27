package flux

import (
	"strings"
	"testing"
)

func TestParseKnobsTolerant(t *testing.T) {
	// A clean directive.
	if k := ParseKnobs("clauses=true canonical_writes=true"); !k.Clauses || !k.CanonicalWrites {
		t.Fatalf("clean directive misparsed: %+v", k)
	}
	// Wrapped in prose, mixed spelling.
	got := ParseKnobs("I'd try clauses=TRUE with canonical_writes=on because canonicality is weak.")
	if !got.Clauses || !got.CanonicalWrites {
		t.Fatalf("prose-wrapped directive misparsed: %+v", got)
	}
	// Unmentioned knobs default to baseline.
	if k := ParseKnobs("canonical_writes=false"); !k.Clauses || k.CanonicalWrites {
		t.Fatalf("defaults not applied: %+v", k)
	}
	// terse
	if k := ParseKnobs("clauses=off canonical_writes=off"); k.Clauses {
		t.Fatalf("off must be false: %+v", k)
	}
}

func TestKnobsMaterializeGrammar(t *testing.T) {
	layout := canonLayout
	// canonical knob must produce the fixed-write-slot grammar (same as GBNFCanonical).
	k := LanguageKnobs{Clauses: true, CanonicalWrites: true}
	if k.Grammar()(layout, KindCompute) != GBNFCanonical(layout, KindCompute) {
		t.Fatal("CanonicalWrites knob must materialize the canonical grammar")
	}
	// baseline knob = GBNF.
	if BaselineKnobs().Grammar()(layout, KindCompute) != GBNF(layout, KindCompute) {
		t.Fatal("baseline knob must materialize the baseline grammar")
	}
	if k.Variant().Name != "clauses+canonical" {
		t.Fatalf("unexpected variant name %q", k.Variant().Name)
	}
}

func TestProposeNextVariantParsesModel(t *testing.T) {
	board := []Score{
		{Variant: "baseline(clauses)", ValidRate: 1.0, MeanTokens: 97, Canonicality: 0.20},
		{Variant: "terse(no-clauses)", ValidRate: 0.73, MeanTokens: 55, Canonicality: 0.28},
	}
	var seenPrompt string
	ask := func(p string) (string, error) {
		seenPrompt = p
		return "clauses=true canonical_writes=true\nRationale: canonicality is the weak axis; fixing write order should raise it.", nil
	}
	prop, err := ProposeNextVariant(board, ask)
	if err != nil {
		t.Fatal(err)
	}
	if !prop.Knobs.Clauses || !prop.Knobs.CanonicalWrites {
		t.Fatalf("proposal knobs misparsed: %+v", prop.Knobs)
	}
	if !strings.Contains(prop.Rationale, "canonicality") {
		t.Fatalf("rationale not extracted: %q", prop.Rationale)
	}
	// The prompt must actually show the model its scoreboard + the fitness definition.
	if !strings.Contains(seenPrompt, "baseline(clauses)") || !strings.Contains(seenPrompt, "canonicality") {
		t.Fatal("proposer prompt must include the board and fitness definition")
	}
}

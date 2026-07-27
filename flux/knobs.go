package flux

// LANGUAGE KNOBS are the executable design space a language-evolution proposer
// searches: each knob is a decision about the grammar the model generates under, and
// a knob configuration materializes into a concrete Variant the scoreboard can score
// and the epoch gate can promote (docs/language-evolution.md). Keeping the space a
// small, typed set of knobs — rather than free-form grammar text — makes a
// model-proposed change safe to execute: the model reasons about the tradeoffs and
// picks a point in the space; the code owns turning that into a valid grammar.

import (
	"fmt"
	"strings"
)

// LanguageKnobs is one point in the language design space.
type LanguageKnobs struct {
	// Clauses includes the explicit (reads …)/(writes …) clauses. true is the
	// baseline; false is the terse (derived) variant — cheaper but validity drops.
	Clauses bool
	// CanonicalWrites emits fixed-order per-field write slots instead of a free-order
	// write-pair list, collapsing the N! orderings of the same update to one — the
	// canonicality lever.
	CanonicalWrites bool
}

// BaselineKnobs is the current live language: explicit clauses, free-order writes.
func BaselineKnobs() LanguageKnobs { return LanguageKnobs{Clauses: true} }

// Directive renders knobs as the compact key=value line the proposer emits and
// ParseKnobs reads — no JSON (the model authors a directive, not a document).
func (k LanguageKnobs) Directive() string {
	return fmt.Sprintf("clauses=%t canonical_writes=%t", k.Clauses, k.CanonicalWrites)
}

// Name is a short, stable label for a knob configuration (used on the scoreboard).
func (k LanguageKnobs) Name() string {
	parts := []string{}
	if k.Clauses {
		parts = append(parts, "clauses")
	} else {
		parts = append(parts, "terse")
	}
	if k.CanonicalWrites {
		parts = append(parts, "canonical")
	}
	return strings.Join(parts, "+")
}

// Grammar materializes the knobs into the grammar function a Variant needs.
func (k LanguageKnobs) Grammar() func(Layout, CellKind) string {
	return func(l Layout, kind CellKind) string {
		return gbnf(l, kind, gbnfOpts{terse: !k.Clauses, canonicalWrites: k.CanonicalWrites})
	}
}

// Variant turns a knob configuration into a scoreboard Variant.
func (k LanguageKnobs) Variant() Variant {
	return Variant{Name: k.Name(), Grammar: k.Grammar()}
}

// ParseKnobs tolerantly extracts a knob configuration from arbitrary model text: it
// scans for `clauses=<bool>` and `canonical_writes=<bool>` anywhere in the string,
// defaulting to the baseline for any knob the model did not mention. So a proposal
// wrapped in prose or a rationale still parses.
func ParseKnobs(text string) LanguageKnobs {
	k := BaselineKnobs()
	if v, ok := scanBool(text, "clauses"); ok {
		k.Clauses = v
	}
	if v, ok := scanBool(text, "canonical_writes"); ok {
		k.CanonicalWrites = v
	}
	return k
}

// scanBool finds `key=<bool>` (case-insensitive, tolerant of surrounding text) and
// returns its value. Accepts true/false, on/off, yes/no, 1/0.
func scanBool(text, key string) (bool, bool) {
	low := strings.ToLower(text)
	i := strings.Index(low, strings.ToLower(key)+"=")
	if i < 0 {
		return false, false
	}
	rest := low[i+len(key)+1:]
	rest = strings.TrimLeft(rest, " ")
	for _, tok := range []struct {
		s string
		v bool
	}{{"true", true}, {"false", false}, {"on", true}, {"off", false}, {"yes", true}, {"no", false}, {"1", true}, {"0", false}} {
		if strings.HasPrefix(rest, tok.s) {
			return tok.v, true
		}
	}
	return false, false
}

// KnobMenu describes the design space to the proposer — the axes it may move and
// what each costs, so the model's proposal is grounded in the actual grammar.
const KnobMenu = `LANGUAGE KNOBS you may set (reply with a directive line "clauses=<bool> canonical_writes=<bool>"):
  clauses          — include explicit (reads …)/(writes …) clauses. true anchors validity + field vocabulary; false (terse) saves tokens but validity has dropped in past measurements.
  canonical_writes — emit fixed-order per-field write slots instead of a free-order write-pair list, so the same set of updates has exactly ONE spelling. Targets low canonicality (the model scattering across equivalent programs); should not affect validity.`

package evolution

// LINEAGE is the derivation graph recorded as a byproduct of the forward process:
// for every derived cell we record the content-hashes of the inputs it was authored
// from — the language, the grammar, the prompt template, the model, the worked
// examples a tool call retrieved, the scenarios it was verified against, the app
// contract, a parent it fractured from, and any leaves it dispatches. See
// docs/lineage.md.
//
// Two things fall out of recording these edges:
//   - PROVENANCE (forward): given a result genotype, what produced it — LineageOf.
//   - AUTHORING MEMO (rebuild): given a bundle of inputs, is there already a cached
//     result — FindLineageByInputs. Because LLM authoring is nondeterministic a cache
//     HIT must REUSE the stored genotype (a re-run would not reproduce the bytes), so
//     the record stores the Result hash; the genotype itself lives in CAS under it.
//
// A change to any input becomes one operation: rebuild the transitive closure of
// what depended on it, reuse everything whose InputsHash is unchanged (docs/lineage.md
// §5). A language change is the extreme case (docs/language-evolution.md).

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"

	"github.com/mdcfrancis/flow/storage"
)

const lineageRef = "urn:hdm:lineage"

// Lineage is one node of the derivation graph: a derived result plus the content
// hashes of the inputs it was derived from. Keyed by Result (the genotype hash), so
// the graph is immutable and append-only.
type Lineage struct {
	Result    string   `json:"result"`              // genotype hash of the derived cell (the key)
	URN       string   `json:"urn,omitempty"`       // cell identity (provenance)
	Language  string   `json:"language,omitempty"`  // hash of the language front-end / version
	Grammar   string   `json:"grammar,omitempty"`   // hash of the GBNF it was decoded under
	Prompt    string   `json:"prompt,omitempty"`    // hash of the prompt TEMPLATE (not inlined Flux)
	Model     string   `json:"model,omitempty"`     // model id + decode params
	Examples  []string `json:"examples,omitempty"`  // ids of worked examples retrieved during authoring
	Scenarios string   `json:"scenarios,omitempty"` // hash of the acceptance suite it was verified against
	Contract  string   `json:"contract,omitempty"`  // hash of the app's shared-state contract
	Parent    string   `json:"parent,omitempty"`    // genotype hash it fractured from, if any
	Leaves    []string `json:"leaves,omitempty"`    // urns it dispatches, if a combinator
}

// InputsHash is the content address of the DERIVATION — a stable hash over every
// input edge (order-independent for the sets). Two authorings with the same inputs
// share an InputsHash, which is what the rebuild memo keys on (docs/lineage.md §2):
// an unchanged InputsHash is a cache hit → reuse the stored Result.
func (l Lineage) InputsHash() string {
	ex := append([]string(nil), l.Examples...)
	sort.Strings(ex)
	lv := append([]string(nil), l.Leaves...)
	sort.Strings(lv)
	parts := []string{
		"lang=" + l.Language,
		"grammar=" + l.Grammar,
		"prompt=" + l.Prompt,
		"model=" + l.Model,
		"examples=" + strings.Join(ex, ","),
		"scenarios=" + l.Scenarios,
		"contract=" + l.Contract,
		"parent=" + l.Parent,
		"leaves=" + strings.Join(lv, ","),
	}
	h := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(h[:])
}

// LoadLineages returns every recorded lineage node (nil if none).
func LoadLineages(ledger *storage.LedgerEngine) []Lineage {
	var ls []Lineage
	loadCollection(ledger, lineageRef, &ls)
	return ls
}

// RecordLineage appends (or replaces) the lineage node for a result, keyed by
// Result hash. Best-effort and idempotent: re-recording the same result overwrites
// its edges (a re-authoring under changed inputs updates the record in place). A
// record with no Result is dropped — there is nothing to key it by.
func RecordLineage(ledger *storage.LedgerEngine, l Lineage) error {
	if ledger == nil || strings.TrimSpace(l.Result) == "" {
		return nil
	}
	ls := LoadLineages(ledger)
	for i, x := range ls {
		if x.Result == l.Result {
			ls[i] = l
			return saveCollection(ledger, lineageRef, ls)
		}
	}
	return saveCollection(ledger, lineageRef, append(ls, l))
}

// LineageOf returns the derivation of a result (its genotype hash), and whether one
// was recorded — the PROVENANCE query ("what was this cell derived from?").
func LineageOf(ledger *storage.LedgerEngine, result string) (Lineage, bool) {
	for _, l := range LoadLineages(ledger) {
		if l.Result == result {
			return l, true
		}
	}
	return Lineage{}, false
}

// FindLineageByInputs returns the most recently recorded result whose derivation
// inputs hash to inputsHash, and whether one exists — the AUTHORING-MEMO query used
// by the rebuild (docs/lineage.md §5): a hit means "these exact inputs already
// produced a cell; reuse its genotype instead of re-authoring." Later records win so
// a re-authoring under the same inputs supersedes an earlier one.
func FindLineageByInputs(ledger *storage.LedgerEngine, inputsHash string) (Lineage, bool) {
	ls := LoadLineages(ledger)
	for i := len(ls) - 1; i >= 0; i-- {
		if ls[i].InputsHash() == inputsHash {
			return ls[i], true
		}
	}
	return Lineage{}, false
}

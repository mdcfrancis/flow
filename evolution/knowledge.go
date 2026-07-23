package evolution

// The KNOWLEDGE BASE is the system's growable, two-tier memory for synthesis:
//   - Example  (episodic): a concrete worked WAT solution — "here is code that passes."
//     Captured from the system's own green cells (plus a few hand-written seeds).
//   - Document (semantic): reusable prose on how to build an architectural concept —
//     "here is how/why to approach this class of problem."
// Both are ledger-backed, retrievable by a cell's kind + intent, and injected into
// synthesis. They are UNTRUSTED guidance: whatever the model produces still runs the
// full compile -> gauntlet -> chaos -> commit gates, so a wrong entry can only mislead
// a candidate that verification then rejects.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"

	"github.com/mdcfrancis/flow/storage"
)

const (
	examplesRef = "urn:hdm:examples"
	docsRef     = "urn:hdm:docs"
)

// Example is a captured worked WAT solution.
type Example struct {
	ID         string   `json:"id"`
	Kind       string   `json:"kind"`  // compute|render|input|leaf (string; no appgen dep)
	Entry      string   `json:"entry"` // run-tick|render-frame
	Semantics  string   `json:"semantics"`
	Reads      []string `json:"reads,omitempty"`
	Writes     []string `json:"writes,omitempty"`
	WAT        string   `json:"wat"`
	Score      string   `json:"score"`      // "5/5" — proof it is a WORKED example
	Provenance string   `json:"provenance"` // source cell URN / objective / "seed"
	Tags       []string `json:"tags,omitempty"`
}

// Document is reusable conceptual knowledge — prose, not code.
type Document struct {
	ID         string   `json:"id"`
	Title      string   `json:"title"`
	Topic      string   `json:"topic"`
	Kinds      []string `json:"kinds,omitempty"` // relevant cell kinds (empty = all)
	Body       string   `json:"body"`
	SeeAlso    []string `json:"seeAlso,omitempty"`
	Provenance string   `json:"provenance"` // "seed" | operator | distilled-from-<cell>
}

// contentID hashes the meaningful content so identical entries dedup and get a stable id.
func contentID(parts ...string) string {
	h := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(h[:8])
}

// --- persistence: each store is one JSON collection under a ref (small, curated) ---

func loadCollection(ledger *storage.LedgerEngine, ref string, out any) {
	if ledger == nil {
		return
	}
	if h, err := ledger.GetRef(ref); err == nil {
		if raw, rerr := ledger.ReadBlock(h); rerr == nil {
			_ = json.Unmarshal(raw, out)
		}
	}
}

func saveCollection(ledger *storage.LedgerEngine, ref string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	h, err := ledger.WriteBlock(raw)
	if err != nil {
		return err
	}
	return ledger.UpdateRef(ref, h)
}

// LoadExamples returns every stored example (nil if none).
func LoadExamples(ledger *storage.LedgerEngine) []Example {
	var xs []Example
	loadCollection(ledger, examplesRef, &xs)
	return xs
}

// LoadDocuments returns every stored document (nil if none).
func LoadDocuments(ledger *storage.LedgerEngine) []Document {
	var ds []Document
	loadCollection(ledger, docsRef, &ds)
	return ds
}

// SaveExamples / SaveDocuments replace the stored collection wholesale.
func SaveExamples(ledger *storage.LedgerEngine, xs []Example) error {
	return saveCollection(ledger, examplesRef, xs)
}
func SaveDocuments(ledger *storage.LedgerEngine, ds []Document) error {
	return saveCollection(ledger, docsRef, ds)
}

// AddExample inserts a worked example under the NOVELTY GATE: it is kept only when it
// adds coverage the store lacks — a new (kind, tag-set) shape, or a strictly better
// (higher-passing, then smaller) solution for a shape already present. This keeps the
// library sharp so retrieval stays high-signal. Returns whether it was stored.
func AddExample(ledger *storage.LedgerEngine, e Example) (bool, error) {
	if strings.TrimSpace(e.WAT) == "" {
		return false, nil
	}
	if e.ID == "" {
		e.ID = contentID(e.Kind, e.Semantics, e.WAT)
	}
	xs := LoadExamples(ledger)
	for i, x := range xs {
		if x.ID == e.ID {
			return false, nil // identical solution already stored
		}
		if x.Kind == e.Kind && sameShape(x, e) {
			if betterExample(e, x) { // strictly better solution for this shape → replace
				xs[i] = e
				return true, SaveExamples(ledger, xs)
			}
			return false, nil // shape already covered by an equal/better example
		}
	}
	return true, SaveExamples(ledger, append(xs, e))
}

// AddDocument inserts/updates a document, keyed by Topic (one strong doc per topic).
func AddDocument(ledger *storage.LedgerEngine, d Document) error {
	if d.ID == "" {
		d.ID = contentID(d.Topic, d.Title)
	}
	ds := LoadDocuments(ledger)
	for i, x := range ds {
		if x.Topic == d.Topic {
			ds[i] = d // replace the topic's doc
			return SaveDocuments(ledger, ds)
		}
	}
	return SaveDocuments(ledger, append(ds, d))
}

// sameShape reports whether two examples cover the same synthesis shape: same entry and
// same tag set (tags name the pattern, e.g. "draw-at-position", "wall-bounce"). An
// untagged example (a raw capture) has no declared shape, so it is only deduped by exact
// content, never collapsed against another — additive but content-unique.
func sameShape(a, b Example) bool {
	if a.Entry != b.Entry || len(a.Tags) == 0 || len(b.Tags) == 0 {
		return false
	}
	return strings.EqualFold(strings.Join(sortedLower(a.Tags), ","), strings.Join(sortedLower(b.Tags), ","))
}

// betterExample reports whether e should replace x for the same shape: more checks
// passing, then (tie) smaller code.
func betterExample(e, x Example) bool {
	ep, en := scoreFrac(e.Score)
	xp, xn := scoreFrac(x.Score)
	if en > 0 && xn > 0 && ep*xn != xp*en {
		return ep*xn > xp*en // higher pass ratio
	}
	return len(e.WAT) < len(x.WAT)
}

func scoreFrac(s string) (pass, total int) {
	if i := strings.IndexByte(s, '/'); i > 0 {
		pass = atoiSafe(s[:i])
		total = atoiSafe(s[i+1:])
	}
	return
}

func atoiSafe(s string) int {
	n := 0
	for _, r := range strings.TrimSpace(s) {
		if r < '0' || r > '9' {
			return n
		}
		n = n*10 + int(r-'0')
	}
	return n
}

func sortedLower(ss []string) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = strings.ToLower(strings.TrimSpace(s))
	}
	sort.Strings(out)
	return out
}

// --- retrieval: rank by kind (hard filter) then intent/shape overlap ---

// FindExamples returns up to k worked examples relevant to a cell of the given kind and
// intent, best first. Kind is a hard filter (a render target only sees render examples);
// ties break on keyword overlap between the query and the example's semantics/tags.
func FindExamples(ledger *storage.LedgerEngine, kind, intent string, reads, writes []string, k int) []Example {
	terms := terms(intent, reads, writes)
	type scored struct {
		e Example
		s int
	}
	var ranked []scored
	for _, e := range LoadExamples(ledger) {
		if kind != "" && e.Kind != "" && !strings.EqualFold(e.Kind, kind) {
			continue
		}
		ranked = append(ranked, scored{e, overlap(terms, e.Semantics, strings.Join(e.Tags, " "), strings.Join(e.Reads, " "))})
	}
	sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].s > ranked[j].s })
	out := make([]Example, 0, k)
	for _, r := range ranked {
		if len(out) >= k {
			break
		}
		out = append(out, r.e)
	}
	return out
}

// FindDocuments returns up to k documents relevant to the kind + intent, best first.
// A document with no Kinds is relevant to all; otherwise the kind must be listed.
func FindDocuments(ledger *storage.LedgerEngine, kind, intent string, k int) []Document {
	terms := terms(intent, nil, nil)
	type scored struct {
		d Document
		s int
	}
	var ranked []scored
	for _, d := range LoadDocuments(ledger) {
		if kind != "" && len(d.Kinds) > 0 && !containsFold(d.Kinds, kind) {
			continue
		}
		ranked = append(ranked, scored{d, overlap(terms, d.Title, d.Topic, d.Body)})
	}
	sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].s > ranked[j].s })
	out := make([]Document, 0, k)
	for _, r := range ranked {
		if len(out) >= k {
			break
		}
		out = append(out, r.d)
	}
	return out
}

// terms tokenizes the query intent + port names into distinctive lowercase words.
func terms(intent string, reads, writes []string) map[string]bool {
	m := map[string]bool{}
	add := func(s string) {
		for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
			return !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9')
		}) {
			if len(w) >= 3 && !stopword[w] {
				m[w] = true
			}
		}
	}
	add(intent)
	for _, r := range reads {
		add(r)
	}
	for _, w := range writes {
		add(w)
	}
	return m
}

func overlap(terms map[string]bool, fields ...string) int {
	hay := strings.ToLower(strings.Join(fields, " "))
	n := 0
	for t := range terms {
		if strings.Contains(hay, t) {
			n++
		}
	}
	return n
}

func containsFold(ss []string, v string) bool {
	for _, s := range ss {
		if strings.EqualFold(s, v) {
			return true
		}
	}
	return false
}

var stopword = map[string]bool{
	"the": true, "and": true, "for": true, "with": true, "that": true, "this": true,
	"from": true, "into": true, "each": true, "its": true, "are": true, "read": true,
	"write": true, "set": true, "cell": true, "value": true,
}

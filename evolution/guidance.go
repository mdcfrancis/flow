package evolution

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"strings"

	"github.com/mdcfrancis/flow/storage"
)

// Guidance is user commentary, rewritten into durable, injectable principles at one scope:
// SYSTEM (a cross-app "constitution" injected into every cell's synthesis) or one APP. It is
// SOFT — guidance the model weighs while building — while an adversarial feedback critic
// enforces the same statements as criteria. The human is a source of criteria, not a judge.

// SystemGuidanceKey is the ledger ref for global, cross-app guidance.
const SystemGuidanceKey = "urn:hdm:system:guidance"

// AppGuidanceKey is the ledger ref for one application's guidance.
func AppGuidanceKey(namespace string) string { return namespace + ":guidance" }

// GuidanceEntry is one rewritten, injectable principle/criterion.
type GuidanceEntry struct {
	ID        string `json:"id"`
	Statement string `json:"statement"`
}

// Guidance is an ordered, de-duplicated set of entries at one scope.
type Guidance struct {
	Entries []GuidanceEntry `json:"entries"`
}

func guidanceID(s string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(strings.ToLower(strings.TrimSpace(s))))
	return fmt.Sprintf("%08x", h.Sum32())
}

// Add appends a statement (de-duplicated by content); returns the entry and whether it was new.
func (g *Guidance) Add(statement string) (GuidanceEntry, bool) {
	statement = strings.TrimSpace(statement)
	if statement == "" {
		return GuidanceEntry{}, false
	}
	id := guidanceID(statement)
	for _, e := range g.Entries {
		if e.ID == id {
			return e, false
		}
	}
	e := GuidanceEntry{ID: id, Statement: statement}
	g.Entries = append(g.Entries, e)
	return e, true
}

// Remove drops the entry with id (for reversibility); returns whether it was present.
func (g *Guidance) Remove(id string) bool {
	for i, e := range g.Entries {
		if e.ID == id {
			g.Entries = append(g.Entries[:i], g.Entries[i+1:]...)
			return true
		}
	}
	return false
}

// Render formats the guidance under a header for injection into a prompt, or "" if empty.
func (g *Guidance) Render(header string) string {
	if g == nil || len(g.Entries) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(header)
	b.WriteByte('\n')
	for _, e := range g.Entries {
		fmt.Fprintf(&b, "- %s\n", e.Statement)
	}
	return b.String()
}

// SaveGuidance persists a guidance set at key.
func SaveGuidance(ledger *storage.LedgerEngine, key string, g *Guidance) error {
	raw, err := json.Marshal(g)
	if err != nil {
		return err
	}
	h, err := ledger.WriteBlock(raw)
	if err != nil {
		return err
	}
	return ledger.UpdateRef(key, h)
}

// LoadGuidance returns the guidance set at key, or an empty (non-nil) set if none.
func LoadGuidance(ledger *storage.LedgerEngine, key string) *Guidance {
	h, err := ledger.GetRef(key)
	if err != nil {
		return &Guidance{}
	}
	raw, err := ledger.ReadBlock(h)
	if err != nil {
		return &Guidance{}
	}
	var g Guidance
	if json.Unmarshal(raw, &g) != nil {
		return &Guidance{}
	}
	return &g
}

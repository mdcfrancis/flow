package evolution

import (
	"strings"

	"github.com/mdcfrancis/flow/storage"
)

func criticNoteRef(urn string) string { return urn + ":critic-note" }

// SaveCriticNote persists a critic's most recent feedback about a cell — why an adversarial
// reviewer (visual OR code) judged its last version unsatisfactory — so the cell's NEXT build
// sees what was wrong instead of re-rolling blind. An empty note clears it (judged good).
func SaveCriticNote(ledger *storage.LedgerEngine, urn, note string) error {
	if strings.TrimSpace(note) == "" {
		return ledger.DeleteRefs(criticNoteRef(urn))
	}
	h, err := ledger.WriteBlock([]byte(note))
	if err != nil {
		return err
	}
	return ledger.UpdateRef(criticNoteRef(urn), h)
}

// LoadCriticNote returns the last critic feedback for a cell, or "" if none.
func LoadCriticNote(ledger *storage.LedgerEngine, urn string) string {
	h, err := ledger.GetRef(criticNoteRef(urn))
	if err != nil {
		return ""
	}
	raw, err := ledger.ReadBlock(h)
	if err != nil {
		return ""
	}
	return string(raw)
}

package evolution

// The SURFACE the operational sieve authors in is promotable ledger state, not just
// an env flag. It defaults to "sexpr"; a language epoch (PromoteSurface) flips it to
// "forth" only after the whole stack re-authors green in the new surface — the
// acceptance discipline of docs/language-evolution.md §3 applied to a surface change.

import (
	"strings"
	"sync"

	"github.com/mdcfrancis/flow/storage"
)

const surfaceRef = "urn:hdm:language:surface"

var (
	surfaceMu     sync.RWMutex
	activeSurfaceName = "sexpr" // set by InstallSurface at boot; overridden during a promotion rebuild
)

// LoadSurface returns the ledger-stored default surface, or "sexpr" if none set.
func LoadSurface(ledger *storage.LedgerEngine) string {
	if ledger == nil {
		return "sexpr"
	}
	if h, err := ledger.GetRef(surfaceRef); err == nil {
		if raw, rerr := ledger.ReadBlock(h); rerr == nil && len(raw) > 0 {
			if s := strings.TrimSpace(string(raw)); s != "" {
				return s
			}
		}
	}
	return "sexpr"
}

// SaveSurface persists the default surface (the promote step of an accepted surface
// epoch).
func SaveSurface(ledger *storage.LedgerEngine, name string) error {
	h, err := ledger.WriteBlock([]byte(name))
	if err != nil {
		return err
	}
	return ledger.UpdateRef(surfaceRef, h)
}

// InstallSurface loads the promoted default surface from the ledger and makes it the
// active surface for synthesis. Called once at boot.
func InstallSurface(ledger *storage.LedgerEngine) {
	setActiveSurface(LoadSurface(ledger))
}

func setActiveSurface(name string) {
	if name != "forth" {
		name = "sexpr"
	}
	surfaceMu.Lock()
	activeSurfaceName = name
	surfaceMu.Unlock()
}

func currentSurface() string {
	surfaceMu.RLock()
	defer surfaceMu.RUnlock()
	return activeSurfaceName
}

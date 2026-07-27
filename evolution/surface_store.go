package evolution

// The SURFACE the operational sieve authors in is promotable ledger state, not just
// an env flag. The OPERATIONAL STANDARD is now "forth" — the type-stratified
// concatenative surface that measured a large LLM-efficiency win (docs/
// flux-surface-ir.md); a running system authors cells in it by default. (Unit tests
// use the package default "sexpr" — see activeSurfaceName — since they were written
// against the S-expression surface; the operational default comes from the ledger via
// InstallSurface at boot.) PromoteSurface remains the audited path for re-authoring an
// existing stack into a surface.

import (
	"strings"
	"sync"

	"github.com/mdcfrancis/flow/flux"
	"github.com/mdcfrancis/flow/storage"
)

const surfaceRef = "urn:hdm:language:surface"

var (
	surfaceMu         sync.RWMutex
	activeSurfaceName = "sexpr" // set by InstallSurface at boot; overridden during a promotion rebuild
)

// LoadSurface returns the ledger-stored default surface, or the standard "forth" if
// none has been set — so a fresh system authors in Forth. An existing ledger that
// pinned a surface (via PromoteSurface) keeps its choice.
func LoadSurface(ledger *storage.LedgerEngine) string {
	if ledger == nil {
		return "forth"
	}
	if h, err := ledger.GetRef(surfaceRef); err == nil {
		if raw, rerr := ledger.ReadBlock(h); rerr == nil && len(raw) > 0 {
			if s := strings.TrimSpace(string(raw)); s != "" {
				return s
			}
		}
	}
	return "forth"
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

// --- data-driven S-expr surfaces (the parser as ledger data) --------------------
//
// An S-expr-family surface can be defined purely as a flux.SurfaceSpec (keyword
// lexicon + clause policy) and stored in the ledger, so a new surface in this family
// is DATA the system holds and can evolve — the analog of the ledger-resident
// prologue (docs/flux-surface-ir.md). A generic flux.SpecSurface interprets it.

const surfaceSpecRef = "urn:hdm:language:surface-specs"

// LoadSurfaceSpecs returns every stored surface spec (nil if none).
func LoadSurfaceSpecs(ledger *storage.LedgerEngine) []flux.SurfaceSpec {
	var specs []flux.SurfaceSpec
	loadCollection(ledger, surfaceSpecRef, &specs)
	return specs
}

// SaveSurfaceSpec upserts a surface spec by name.
func SaveSurfaceSpec(ledger *storage.LedgerEngine, spec flux.SurfaceSpec) error {
	specs := LoadSurfaceSpecs(ledger)
	for i := range specs {
		if specs[i].Name == spec.Name {
			specs[i] = spec
			return saveCollection(ledger, surfaceSpecRef, specs)
		}
	}
	return saveCollection(ledger, surfaceSpecRef, append(specs, spec))
}

// LoadSurfaceSpec returns a stored spec by name and whether it exists.
func LoadSurfaceSpec(ledger *storage.LedgerEngine, name string) (flux.SurfaceSpec, bool) {
	for _, s := range LoadSurfaceSpecs(ledger) {
		if s.Name == name {
			return s, true
		}
	}
	return flux.SurfaceSpec{}, false
}

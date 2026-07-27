package evolution

// The prologue — the language's derived vocabulary (neg/abs/min/max/clamp and any
// evolved words) — is LEDGER-RESIDENT: stored as data under a ref, loaded at boot,
// and installed into the flux front-end. This is the deepest push of the language
// onto the cell substrate (docs/flux-surface-ir.md, docs/lineage.md): the derived
// vocabulary is no longer fixed Go; the running system reads it from its own store and
// can rewrite it (SavePrologue) — a language change the epoch gate can then accept.

import (
	"github.com/mdcfrancis/flow/flux"
	"github.com/mdcfrancis/flow/storage"
)

const prologueRef = "urn:hdm:language:prologue"

// LoadPrologue returns the ledger-stored derivations (nil if none seeded yet).
func LoadPrologue(ledger *storage.LedgerEngine) []flux.Derivation {
	var ds []flux.Derivation
	loadCollection(ledger, prologueRef, &ds)
	return ds
}

// SavePrologue replaces the stored derived vocabulary. A subsequent InstallPrologue
// (or boot) makes it live — this is how the system rewrites its own language.
func SavePrologue(ledger *storage.LedgerEngine, ds []flux.Derivation) error {
	return saveCollection(ledger, prologueRef, ds)
}

// InstallPrologue loads the derived vocabulary from the ledger — seeding the built-in
// default on first run — and installs it into the flux front-end, so lowering expands
// against the ledger-resident prologue. Called once at boot, after the ledger is
// online and before synthesis.
func InstallPrologue(ledger *storage.LedgerEngine) error {
	ds := LoadPrologue(ledger)
	if len(ds) == 0 {
		ds = flux.DefaultPrologue
		if err := SavePrologue(ledger, ds); err != nil {
			return err
		}
	}
	return flux.SetPrologue(ds)
}

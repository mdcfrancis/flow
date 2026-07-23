package evolution

// Persistent, root-keyed convergence. A cell the loop can no longer improve is
// marked converged — but convergence is recorded RELATIVE to the manifest root
// at the time (the global system state). Persisting this to the ledger means
// (a) convergence survives a restart instead of being re-derived, and (b) when
// the system moves on (another cell commits, advancing the root), a cell
// converged against the OLD root is stale and is re-evaluated in the new
// context. The system only truly rests at a global fixpoint: every cell
// converged at the SAME, current root.

import (
	"encoding/json"
	"fmt"

	"github.com/mdcfrancis/flow/storage"
)

// FrictionState is a cell's persisted convergence record.
type FrictionState struct {
	Converged bool `json:"converged"` // parked: no progress for convergenceHolds frames
	// Incomplete distinguishes STALLED (parked while still failing acceptance
	// checks — the model is stuck, needs more attempts) from truly converged
	// (complete: nothing left to build, only optimization was exhausted).
	Incomplete bool   `json:"incomplete,omitempty"`
	Holds      int    `json:"holds"`             // consecutive no-progress frames
	Retries    int    `json:"retries,omitempty"` // stalled-cell retry attempts spent
	Root       string `json:"root"`              // manifest root when recorded
}

// Parked reports whether the cell is out of the active selection at the current
// root — converged (done) or stalled (incomplete), but recorded against this
// system state. A parked cell re-activates when the root advances.
func (f FrictionState) Parked(currentRoot string) bool {
	return f.Converged && f.Root == currentRoot
}

// Settled reports whether the cell is genuinely at rest: parked AND complete.
// A stalled (incomplete) cell is never settled — it still has work to do.
func (f FrictionState) Settled(currentRoot string) bool {
	return f.Parked(currentRoot) && !f.Incomplete
}

// Stalled reports whether the cell is parked but incomplete (the model could not
// finish building it) at the current root — a candidate for a retry.
func (f FrictionState) Stalled(currentRoot string) bool {
	return f.Parked(currentRoot) && f.Incomplete
}

func frictionRefURN(cell string) string { return cell + ":friction" }

// SaveFriction persists a cell's convergence record.
func SaveFriction(ledger *storage.LedgerEngine, cell string, fs FrictionState) error {
	raw, err := json.Marshal(fs)
	if err != nil {
		return fmt.Errorf("encode friction: %w", err)
	}
	h, err := ledger.WriteBlock(raw)
	if err != nil {
		return fmt.Errorf("persist friction: %w", err)
	}
	return ledger.UpdateRef(frictionRefURN(cell), h)
}

// LoadFriction returns a cell's persisted convergence record, or the zero value
// (not converged) if none is stored.
func LoadFriction(ledger *storage.LedgerEngine, cell string) FrictionState {
	h, err := ledger.GetRef(frictionRefURN(cell))
	if err != nil {
		return FrictionState{}
	}
	raw, err := ledger.ReadBlock(h)
	if err != nil {
		return FrictionState{}
	}
	var fs FrictionState
	if json.Unmarshal(raw, &fs) != nil {
		return FrictionState{}
	}
	return fs
}

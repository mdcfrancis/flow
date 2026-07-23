package evolution

import (
	"path/filepath"
	"testing"

	"github.com/mdcfrancis/flow/storage"
)

func TestMetaAcceptanceGate(t *testing.T) {
	// Defaults correctly classify the trust corpus.
	if !MetaAcceptanceGate(DefaultCheckThresholds()) {
		t.Fatal("default thresholds must pass the gate")
	}
	// A WEAKENING (would accept a known-bad 2-color render) must fail the gate.
	if MetaAcceptanceGate(CheckThresholds{MinColors: 2, MinBrightSpread: 30}) {
		t.Fatal("weak thresholds must fail the gate — a known-bad render would pass")
	}
	// An OVER-tightening (would reject the minimal-good render) must fail the gate.
	if MetaAcceptanceGate(CheckThresholds{MinColors: 11, MinBrightSpread: 110}) {
		t.Fatal("over-tight thresholds must fail the gate — a known-good render would be rejected")
	}
}

func TestCheckThresholdsGatedSaveAndTighten(t *testing.T) {
	le, _ := storage.NewLedgerEngine(filepath.Join(t.TempDir(), "hdm.db"))
	defer le.Close()
	if LoadCheckThresholds(le) != DefaultCheckThresholds() {
		t.Fatal("expected defaults when nothing stored")
	}
	// Save GATES: a weakening is rejected; a valid change is accepted + persisted.
	if err := SaveCheckThresholds(le, CheckThresholds{MinColors: 2, MinBrightSpread: 0}); err == nil {
		t.Fatal("save must reject gate-failing thresholds")
	}
	if err := SaveCheckThresholds(le, CheckThresholds{MinColors: 10, MinBrightSpread: 120}); err != nil {
		t.Fatalf("save must accept gate-passing thresholds: %v", err)
	}
	if got := LoadCheckThresholds(le); got.MinColors != 10 {
		t.Errorf("not persisted: %+v", got)
	}
	// TightenCheck evolves the grader to the strictest the corpus allows (10 colors, 130 spread).
	adopted, changed := TightenCheck(le)
	if !changed || adopted.MinColors != 10 || adopted.MinBrightSpread != 130 {
		t.Fatalf("tighten should reach (10,130): %+v changed=%v", adopted, changed)
	}
	// And the tightened grader still passes its own gate (never gameable).
	if !MetaAcceptanceGate(adopted) {
		t.Fatal("tightened thresholds must still pass the gate")
	}
}

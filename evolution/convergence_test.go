package evolution

import (
	"path/filepath"
	"testing"

	"github.com/mdcfrancis/flow/storage"
)

func TestFrictionSaveLoadRoundTrip(t *testing.T) {
	le, err := storage.NewLedgerEngine(filepath.Join(t.TempDir(), "hdm.db"))
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	defer le.Close()

	// No record yet -> zero value, not converged.
	if fs := LoadFriction(le, "urn:x"); fs.Converged || fs.Holds != 0 {
		t.Fatalf("missing friction should be zero: %+v", fs)
	}

	want := FrictionState{Converged: true, Holds: 3, Root: "R7"}
	if err := SaveFriction(le, "urn:x", want); err != nil {
		t.Fatalf("save: %v", err)
	}
	got := LoadFriction(le, "urn:x")
	if got != want {
		t.Fatalf("roundtrip = %+v, want %+v", got, want)
	}
	if !got.Settled("R7") || got.Settled("R8") {
		t.Fatalf("Settled wrong: settled@R7=%v settled@R8=%v", got.Settled("R7"), got.Settled("R8"))
	}
}

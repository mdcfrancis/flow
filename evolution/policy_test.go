package evolution

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/mdcfrancis/flow/storage"
)

func TestPolicyClampAndPersist(t *testing.T) {
	le, _ := storage.NewLedgerEngine(filepath.Join(t.TempDir(), "hdm.db"))
	defer le.Close()
	// Defaults when nothing stored.
	if p := LoadPolicy(le); !reflect.DeepEqual(p, DefaultPolicy()) {
		t.Fatalf("expected defaults, got %+v", p)
	}
	if PolicyOverridden(le) {
		t.Error("no override should be reported")
	}
	// Out-of-bounds values are clamped on save + load.
	_ = SavePolicy(le, Policy{MaxStallRetries: 999, GrievanceThreshold: 0, VisionIntervalSec: 1, CodeCriticIntervalSec: 99999})
	p := LoadPolicy(le)
	if p.MaxStallRetries != 10 || p.GrievanceThreshold != 1 || p.VisionIntervalSec != 15 || p.CodeCriticIntervalSec != 1800 {
		t.Fatalf("clamp failed: %+v", p)
	}
	if !PolicyOverridden(le) {
		t.Error("override should be reported after save")
	}
	// A partial override merges onto defaults (missing fields keep defaults).
	_ = SavePolicy(le, Policy{MaxStallRetries: 5, GrievanceThreshold: 4, VisionIntervalSec: 60, CodeCriticIntervalSec: 200})
	if p := LoadPolicy(le); p.MaxStallRetries != 5 || p.CodeCriticIntervalSec != 200 {
		t.Fatalf("override not applied: %+v", p)
	}
}

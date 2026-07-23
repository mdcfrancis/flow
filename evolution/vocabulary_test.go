package evolution

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/mdcfrancis/flow/storage"
)

// TestBuildSeedAdvertisesPrimitives: a run-tick cell's build seed lists the reusable primitives
// (so synthesis can reach for them), and a render-frame (UI) cell's does not.
func TestBuildSeedAdvertisesPrimitives(t *testing.T) {
	le, _ := storage.NewLedgerEngine(filepath.Join(t.TempDir(), "hdm.db"))
	defer le.Close()
	o := &Orchestrator{ledger: le}
	suite := &AcceptanceSuite{Scenarios: []Scenario{{Name: "s", Entry: "run-tick"}}}

	runTick := o.buildSeed("urn:hdm:app:x:logic", "do a thing", "(module)", RunTickContract, suite)
	for _, must := range []string{"sys:dict", "sys:list", "invoke-cell"} {
		if !strings.Contains(runTick, must) {
			t.Errorf("run-tick build seed must advertise %q", must)
		}
	}

	ui := o.buildSeed("urn:hdm:app:x:view", "draw", "(module)", RenderFrameContract, suite)
	if strings.Contains(ui, "sys:dict") {
		t.Error("a render-frame (UI) build seed should NOT carry the primitive vocabulary")
	}
}

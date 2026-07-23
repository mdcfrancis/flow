package execution

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mdcfrancis/flow/compiler"
	"github.com/mdcfrancis/flow/inference"
	"github.com/mdcfrancis/flow/storage"
)

// TestPrivatePagesIsolationAndPersistence proves the software-paging ABI:
//   - LOCATION INDEPENDENCE: a cell's physical page is placed away from the window, yet the
//     WAT reaches it through the fixed windowBase — the runtime translates.
//   - PERSISTENCE: a private page round-trips through page-in/page-out across ticks.
//   - STRUCTURAL ISOLATION: a cell mapped to a different page can never see another's data,
//     with mask enforcement OFF (structural) and ON (page zone additionally hidden).
func TestPrivatePagesIsolationAndPersistence(t *testing.T) {
	ctx := context.Background()
	le, _ := storage.NewLedgerEngine(filepath.Join(t.TempDir(), "hdm.db"))
	defer le.Close()
	rm, err := NewRuntimeManager(ctx, le, inference.NewLocalModelClient("http://127.0.0.1:0", "test"))
	if err != nil {
		t.Fatalf("runtime: %v", err)
	}
	defer rm.Close(ctx)
	cs := compiler.NewCompilerService()

	// Every cell treats windowBase (0x00400000) as a plain flat block — no handles, no base
	// math. The runtime maps the right physical page behind it.
	writer := `(module
  (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 100))
  (func (export "run-tick") (param i32 i32) (result i32)
    (i32.store (i32.const 0x00400000) (i32.const 42))
    (i32.load (i32.const 0x00400000))))`
	reader := `(module
  (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 100))
  (func (export "run-tick") (param i32 i32) (result i32)
    (i32.load (i32.const 0x00400000))))`
	bumper := `(module
  (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 100))
  (func (export "run-tick") (param i32 i32) (result i32)
    (i32.store (i32.const 0x00400000) (i32.add (i32.load (i32.const 0x00400000)) (i32.const 1)))
    (i32.load (i32.const 0x00400000))))`
	loadWAT(t, rm, cs, "urn:hdm:test:writer", writer)
	loadWAT(t, rm, cs, "urn:hdm:test:reader", reader)
	loadWAT(t, rm, cs, "urn:hdm:test:bumper", bumper)

	// A junk page first, so the writer's page is NOT at pageZoneBase — proving the runtime
	// translates rather than the guest relying on a fixed physical base.
	if _, err := rm.AllocPage(pageSize); err != nil {
		t.Fatalf("junk alloc: %v", err)
	}
	hW, err := rm.AllocPage(pageSize)
	if err != nil {
		t.Fatalf("writer page: %v", err)
	}
	hR, err := rm.AllocPage(pageSize)
	if err != nil {
		t.Fatalf("reader page: %v", err)
	}
	if base, _ := rm.PageBase(hW); base == windowBase {
		t.Fatalf("writer page base %#x equals the window base — no translation happened", base)
	}

	rm.MapPage("urn:hdm:test:writer", hW)
	rm.MapPage("urn:hdm:test:reader", hR)
	rm.MapPage("urn:hdm:test:bumper", hW) // bumper shares the writer's page

	run := func(urn string) uint32 {
		t.Helper()
		res, _, _, err := rm.ExecuteTrampoline("h", urn, "run-tick", 0, 0)
		if err != nil {
			t.Fatalf("%s: %v", urn, err)
		}
		return res
	}

	// writer stores 42 into its window; page-out persists it to page hW.
	if got := run("urn:hdm:test:writer"); got != 42 {
		t.Fatalf("writer = %d, want 42", got)
	}
	// reader is mapped to a DIFFERENT page: it must NOT see 42 — structural isolation.
	if got := run("urn:hdm:test:reader"); got != 0 {
		t.Fatalf("reader = %d, want 0 (isolation breached)", got)
	}
	// bumper shares the writer's page: sees the persisted 42, leaves 43.
	if got := run("urn:hdm:test:bumper"); got != 43 {
		t.Fatalf("bumper = %d, want 43 (persistence/mapping broken)", got)
	}
	// run again: 43 -> 44, proving page-in/page-out round-trips the page each tick.
	if got := run("urn:hdm:test:bumper"); got != 44 {
		t.Fatalf("bumper second run = %d, want 44", got)
	}
	// reader STILL isolated after writes to hW.
	if got := run("urn:hdm:test:reader"); got != 0 {
		t.Fatalf("reader = %d after writes, want 0", got)
	}

	// With mask enforcement ON the physical page zone is additionally hidden/preserved from
	// direct guest access; paging must still work (page-out ordering vs the mask revert).
	rm.SetMasksEnabled(true)
	if got := run("urn:hdm:test:bumper"); got != 45 {
		t.Fatalf("bumper under masks = %d, want 45 (paging broke under mask enforcement)", got)
	}
	if got := run("urn:hdm:test:reader"); got != 0 {
		t.Fatalf("reader under masks = %d, want 0", got)
	}
}

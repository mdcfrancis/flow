package execution

import (
	"context"
	"encoding/binary"
	"path/filepath"
	"testing"

	"github.com/mdcfrancis/flow/compiler"
	"github.com/mdcfrancis/flow/inference"
	"github.com/mdcfrancis/flow/manifest"
	"github.com/mdcfrancis/flow/storage"
)

// A render cell reaches the runtime ONLY through RenderFrame (the frame loop skips
// render cells), which used to resolve a cell by URN presence alone — so once its
// module was cached it kept executing forever, even after evolution committed a new
// genome/phenotype. Symptom: a converged renderer kept drawing its no-op SEED on
// the canvas while its committed code was correct. RenderFrame must reload when the
// committed phenotype hash changes, like TickAppCell does for run-tick cells.
func TestRenderFrameReloadsOnPhenotypeChange(t *testing.T) {
	ctx := context.Background()
	le, err := storage.NewLedgerEngine(filepath.Join(t.TempDir(), "hdm.db"))
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	defer le.Close()
	rm, err := NewRuntimeManager(ctx, le, inference.NewLocalModelClient("http://127.0.0.1:0", "test"))
	if err != nil {
		t.Fatalf("runtime: %v", err)
	}
	defer rm.Close(ctx)
	repo := manifest.NewRepository(le)
	cs := compiler.NewCompilerService()
	const urn = "urn:hdm:apps:t:renderer"

	// Two render-frame cells that write a single draw record and return its length:
	// v1 a rect (op 257), v2 a circle (op 259). The op sits in the record's first
	// i32, which is what the canvas parser reads.
	const rectWAT = `(module (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 100))
  (func (export "render-frame") (param $b i32) (param $c i32) (result i32)
    (i32.store (local.get $b) (i32.const 257))
    (i32.store offset=20 (local.get $b) (i32.const -1))
    (i32.const 24)))`
	const circleWAT = `(module (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 100))
  (func (export "render-frame") (param $b i32) (param $c i32) (result i32)
    (i32.store (local.get $b) (i32.const 259))
    (i32.store offset=20 (local.get $b) (i32.const -1))
    (i32.const 24)))`

	commit := func(wat string) {
		art, e := cs.CompileGenotype(wat)
		if e != nil || art == nil || !art.SyntaxPassed {
			t.Fatalf("compile: %v", e)
		}
		h, _, e := repo.PutCell(urn, wat, art.Bytecode, manifest.SemanticManifest{}, 0)
		if e != nil {
			t.Fatalf("put: %v", e)
		}
		if e := repo.SeedRef(urn, h); e != nil {
			t.Fatalf("seed: %v", e)
		}
	}
	op := func() int32 {
		frame, e := rm.RenderFrame(urn)
		if e != nil {
			t.Fatalf("render: %v", e)
		}
		if len(frame) < 4 {
			t.Fatalf("short frame: %d bytes", len(frame))
		}
		return int32(binary.LittleEndian.Uint32(frame[0:4]))
	}

	commit(rectWAT)
	if got := op(); got != 257 {
		t.Fatalf("v1: expected rect op 257, got %d", got)
	}
	commit(circleWAT) // evolution commits a new phenotype for the same URN
	if got := op(); got != 259 {
		t.Fatalf("after commit: expected circle op 259, got %d — RenderFrame served a STALE cached module", got)
	}
}

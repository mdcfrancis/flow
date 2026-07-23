package manifest

import (
	"path/filepath"
	"testing"

	"github.com/mdcfrancis/flow/storage"
)

// TestLoadRejectsNonDescriptorRef reproduces the pre-descriptor-schema case: a
// cell reference pointing straight at compiled WASM bytecode (which begins with
// the \x00 magic byte). Load must fail so the boot logic re-seeds it rather than
// crash-looping on JSON decode.
func TestLoadRejectsNonDescriptorRef(t *testing.T) {
	le, err := storage.NewLedgerEngine(filepath.Join(t.TempDir(), "hdm.db"))
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	defer le.Close()
	repo := NewRepository(le)

	h, err := le.WriteBlock([]byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0, 0, 0}) // wasm magic
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := le.UpdateRef("urn:hdm:sys:optimizer", h); err != nil {
		t.Fatalf("ref: %v", err)
	}
	if _, err := repo.Load("urn:hdm:sys:optimizer"); err == nil {
		t.Fatal("Load must reject a ref that points at bytecode, not a descriptor")
	}
}

func TestPutLoadCell(t *testing.T) {
	le, err := storage.NewLedgerEngine(filepath.Join(t.TempDir(), "hdm.db"))
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	defer le.Close()
	repo := NewRepository(le)

	const urn = "urn:hdm:cell:test"
	wat := `(module (func (export "run-tick") (param i32 i32) (result i32) i32.const 1))`
	bytecode := []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}
	sem := SemanticManifest{FunctionalIntent: "test", DomainTags: []string{"unit"}}

	descHash, desc, err := repo.PutCell(urn, wat, bytecode, sem, 0.5)
	if err != nil {
		t.Fatalf("put cell: %v", err)
	}
	if err := repo.SeedRef(urn, descHash); err != nil {
		t.Fatalf("seed ref: %v", err)
	}

	// Genotype and phenotype are distinct content-addressed blocks.
	if desc.GenotypeHash == desc.PhenotypeHash {
		t.Fatal("genotype and phenotype share a hash")
	}

	loaded, err := repo.Load(urn)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.IdentityURN != urn || loaded.Saliency != 0.5 ||
		loaded.Semantics.FunctionalIntent != "test" {
		t.Fatalf("descriptor round-trip mismatch: %+v", loaded)
	}

	gotWAT, err := repo.Genotype(loaded)
	if err != nil || gotWAT != wat {
		t.Fatalf("genotype round-trip: %q, %v", gotWAT, err)
	}
	gotBC, err := repo.Phenotype(loaded)
	if err != nil || string(gotBC) != string(bytecode) {
		t.Fatalf("phenotype round-trip: %v", err)
	}
}

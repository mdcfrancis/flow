package stdlib

import (
	"strings"
	"testing"

	"github.com/mdcfrancis/flow/compiler"
)

// Every embedded raw-WAT standard cell must assemble — a guard so an accidental edit
// to a cells/*.wat file (these are live runtime ABIs) is caught at test time, not at
// boot.
func TestEmbeddedCellsAssemble(t *testing.T) {
	svc := compiler.NewCompilerService()
	names := readDirSorted(cellFS, "cells")
	if len(names) == 0 {
		t.Fatal("no embedded cells found")
	}
	for _, n := range names {
		if !strings.HasSuffix(n, ".wat") {
			continue // .macro cells need expansion against a layout; not asserted here
		}
		body, ok := Cell(strings.TrimSuffix(n, ".wat"))
		if !ok {
			t.Fatalf("Cell(%q) not found", n)
		}
		art, err := svc.CompileGenotype(body)
		if err != nil || art == nil || !art.SyntaxPassed {
			t.Errorf("cell %s did not assemble: %v", n, err)
		}
	}
}

// The embedded prologue and examples load with non-empty bodies and parsed metadata.
func TestEmbeddedLibraryLoads(t *testing.T) {
	if len(Prologue()) == 0 {
		t.Error("no prologue macros embedded")
	}
	for _, m := range Prologue() {
		if !strings.Contains(m.Src, "(defmacro") {
			t.Errorf("prologue macro body is not a defmacro: %q", m.Src)
		}
	}
	exs := Examples()
	if len(exs) == 0 {
		t.Error("no examples embedded")
	}
	for _, e := range exs {
		if strings.TrimSpace(e.Src) == "" || e.Kind == "" || e.Entry == "" {
			t.Errorf("example missing body/metadata: %+v", e)
		}
	}
}

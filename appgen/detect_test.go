package appgen

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mdcfrancis/flow/storage"
)

type detectModel struct{ out string }

func (m detectModel) InvokeReasoning(ctx context.Context, sys, user string) (string, error) {
	return m.out, nil
}

func TestDetectSharedStructure(t *testing.T) {
	le, _ := storage.NewLedgerEngine(filepath.Join(t.TempDir(), "hdm.db"))
	defer le.Close()
	cells := []CellWAT{
		{URN: "urn:hdm:app:a:lookup", WAT: "(module ... linear scan for key ...)"},
		{URN: "urn:hdm:app:b:lookup", WAT: "(module ... linear scan for key ...)"},
	}

	// A grounded proposal naming both real cells is accepted.
	g := NewGrower(le, detectModel{out: `{"urn":"urn:hdm:sys:kv","ops":"[op,key,val]","cells":["urn:hdm:app:a:lookup","urn:hdm:app:b:lookup"],"rationale":"both linear-scan a key"}`})
	p, err := g.DetectSharedStructure(context.Background(), cells)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if p == nil || p.URN != "urn:hdm:sys:kv" || len(p.Cells) != 2 {
		t.Fatalf("expected a grounded 2-cell proposal, got %+v", p)
	}

	// "nothing to promote" -> nil.
	g = NewGrower(le, detectModel{out: `{}`})
	if p, _ := g.DetectSharedStructure(context.Background(), cells); p != nil {
		t.Fatalf("empty proposal must yield nil, got %+v", p)
	}

	// A non-sys URN is dropped.
	g = NewGrower(le, detectModel{out: `{"urn":"urn:hdm:app:kv","cells":["urn:hdm:app:a:lookup","urn:hdm:app:b:lookup"]}`})
	if p, _ := g.DetectSharedStructure(context.Background(), cells); p != nil {
		t.Fatalf("non-sys primitive URN must be dropped, got %+v", p)
	}

	// A proposal naming a hallucinated cell (only 1 real) is dropped.
	g = NewGrower(le, detectModel{out: `{"urn":"urn:hdm:sys:kv","cells":["urn:hdm:app:a:lookup","urn:hdm:app:ghost"]}`})
	if p, _ := g.DetectSharedStructure(context.Background(), cells); p != nil {
		t.Fatalf("a proposal with < 2 real cells must be dropped, got %+v", p)
	}
}

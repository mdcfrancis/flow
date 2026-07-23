package appgen

import (
	"context"
	"testing"

	"github.com/mdcfrancis/flow/compiler"
	"github.com/mdcfrancis/flow/evolution"
	"github.com/mdcfrancis/flow/execution"
)

// TestCombinatorAcceptancePasses proves each enrolled combinator's behavior contract is correct
// and fully passed by the REAL combinator (dispatching the fixture leaves through the sandbox
// resolver) — so enrolling them in evolution is safe: a mutation that breaks one fails its own
// suite and is rejected.
func TestCombinatorAcceptancePasses(t *testing.T) {
	cs := compiler.NewCompilerService()
	bcByURN := map[string][]byte{}
	for _, leaf := range CombinatorTestLeaves() {
		art, err := cs.CompileGenotype(leaf.WAT)
		if err != nil || !art.SyntaxPassed {
			t.Fatalf("leaf %s: %v (%s)", leaf.URN, err, art.ErrorContext)
		}
		bcByURN[leaf.URN] = art.Bytecode
	}
	watByURN := map[string]string{}
	for _, c := range execution.SystemCombinators() {
		watByURN[c.URN] = c.WAT
	}
	resolver := func(urn string) ([]byte, bool) { bc, ok := bcByURN[urn]; return bc, ok }

	for urn, suite := range EvolvableCombinators(evolution.DefaultPayloadOffset) {
		wat, ok := watByURN[urn]
		if !ok {
			t.Fatalf("no WAT for enrolled combinator %s", urn)
		}
		art, err := cs.CompileGenotype(wat)
		if err != nil || !art.SyntaxPassed {
			t.Fatalf("%s: %v (%s)", urn, err, art.ErrorContext)
		}
		passed, total := evolution.ScoreSuite(context.Background(), art.Bytecode, "run-tick", suite,
			evolution.DefaultPayloadOffset, evolution.DefaultStateWindow, resolver)
		if total == 0 || passed != total {
			t.Errorf("%s acceptance must fully pass the real cell, got %d/%d", urn, passed, total)
		}
	}
}

// TestBrokenCombinatorFails confirms the map suite actually pins behavior (a no-op map fails).
func TestBrokenCombinatorFails(t *testing.T) {
	cs := compiler.NewCompilerService()
	bcByURN := map[string][]byte{}
	for _, leaf := range CombinatorTestLeaves() {
		art, _ := cs.CompileGenotype(leaf.WAT)
		bcByURN[leaf.URN] = art.Bytecode
	}
	resolver := func(urn string) ([]byte, bool) { bc, ok := bcByURN[urn]; return bc, ok }
	brokenArt, _ := cs.CompileGenotype(`(module
  (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 1))
  (func (export "run-tick") (param i32 i32) (result i32) (i32.const 0)))`)
	suite := MapAcceptance(evolution.DefaultPayloadOffset)
	bp, bt := evolution.ScoreSuite(context.Background(), brokenArt.Bytecode, "run-tick", suite,
		evolution.DefaultPayloadOffset, evolution.DefaultStateWindow, resolver)
	if bp == bt {
		t.Fatalf("a no-op map must FAIL the acceptance, got %d/%d", bp, bt)
	}
}

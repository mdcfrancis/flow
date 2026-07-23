package appgen

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mdcfrancis/flow/manifest"
	"github.com/mdcfrancis/flow/storage"
)

// scriptedModel returns the envelope on the first call and a fixed skeleton on
// every subsequent call (genesis, including sieve repair iterations).
type scriptedModel struct {
	envelope string
	skeleton string
	calls    int
}

func (m *scriptedModel) InvokeReasoning(ctx context.Context, sys, user string) (string, error) {
	m.calls++
	if m.calls == 1 {
		return m.envelope, nil
	}
	return m.skeleton, nil
}

const goodSkeleton = `(module
  (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 100))
  (func (export "run-tick") (param i32 i32) (result i32) i32.const 0))`

func newGrower(t *testing.T, m Reasoner) (*Grower, *manifest.Repository) {
	t.Helper()
	le, err := storage.NewLedgerEngine(filepath.Join(t.TempDir(), "hdm.db"))
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	t.Cleanup(func() { le.Close() })
	return NewGrower(le, m), manifest.NewRepository(le)
}

func TestGrowScaffoldsSubsystems(t *testing.T) {
	env := `{"application_namespace":"urn:hdm:apps:ratelimiter",
	  "global_constraints":{"target_p99_latency_ns":500000,"max_fuel_allocation_per_transit":50000},
	  "subsystem_requirements":[
	    {"identity":"urn:hdm:apps:ratelimiter:ingress","semantics":"route inbound tokens"},
	    {"identity":"urn:hdm:apps:ratelimiter:evaluator","semantics":"track sliding window"}]}`
	g, repo := newGrower(t, &scriptedModel{envelope: env, skeleton: goodSkeleton})

	envelope, created, _, err := g.Grow(context.Background(), "build a rate limiter")
	if err != nil {
		t.Fatalf("grow: %v", err)
	}
	if envelope.ApplicationNamespace != "urn:hdm:apps:ratelimiter" || len(created) != 2 {
		t.Fatalf("envelope=%+v created=%v", envelope, created)
	}
	// Each subsystem is now a live, resolvable cell with a compiled phenotype.
	for _, urn := range created {
		desc, err := repo.Load(urn)
		if err != nil {
			t.Fatalf("subsystem %s not live: %v", urn, err)
		}
		bc, err := repo.Phenotype(desc)
		if err != nil || len(bc) < 8 || bc[0] != 0x00 || bc[1] != 0x61 {
			t.Fatalf("subsystem %s has no valid phenotype", urn)
		}
	}
}

func TestGrowFallsBackOnBadGenesis(t *testing.T) {
	env := `{"application_namespace":"urn:hdm:apps:x",
	  "subsystem_requirements":[{"identity":"urn:hdm:apps:x:core","semantics":"do the thing"}]}`
	// Genesis model output never compiles => fallback skeleton is used.
	g, repo := newGrower(t, &scriptedModel{envelope: env, skeleton: "this is not wat at all"})

	_, created, _, err := g.Grow(context.Background(), "objective")
	if err != nil {
		t.Fatalf("grow: %v", err)
	}
	if len(created) != 1 {
		t.Fatalf("created = %v, want 1", created)
	}
	desc, err := repo.Load("urn:hdm:apps:x:core")
	if err != nil {
		t.Fatalf("core not live: %v", err)
	}
	if _, err := repo.Phenotype(desc); err != nil {
		t.Fatalf("fallback phenotype missing: %v", err)
	}
}

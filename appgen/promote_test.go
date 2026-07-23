package appgen

import (
	"context"
	"testing"

	"github.com/mdcfrancis/flow/evolution"
	"github.com/mdcfrancis/flow/execution"
)

// TestPromotePrimitiveGate: a correct primitive is admitted, and every failure mode a
// self-proposed primitive could have — wrong behavior, no compile, no suite — is refused.
func TestPromotePrimitiveGate(t *testing.T) {
	ctx := context.Background()
	p, sw := evolution.DefaultPayloadOffset, evolution.DefaultStateWindow

	// Correct primitives pass the gate.
	for _, c := range []struct {
		name  string
		wat   string
		suite *evolution.AcceptanceSuite
	}{
		{"dict", execution.SysDictWAT, DictAcceptance(p)},
		{"list", execution.SysListWAT, ListAcceptance(p)},
		{"set", execution.SysSetWAT, SetAcceptance(p)},
	} {
		_, ok, passed, total, reason := PromotePrimitive(ctx, c.wat, c.suite, p, sw)
		if !ok {
			t.Fatalf("%s should be admitted, got %d/%d: %s", c.name, passed, total, reason)
		}
	}

	// A behaviorally-wrong primitive (always 0) is refused against the dict suite.
	nullWAT := `(module
  (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 100))
  (func (export "run-tick") (param i32 i32) (result i32) (i32.const 0)))`
	if _, ok, _, _, reason := PromotePrimitive(ctx, nullWAT, DictAcceptance(p), p, sw); ok {
		t.Fatalf("a wrong primitive must be refused; got ok (%s)", reason)
	}

	// A non-compiling candidate is refused.
	if _, ok, _, _, reason := PromotePrimitive(ctx, `(module (this is not wat`, DictAcceptance(p), p, sw); ok {
		t.Fatalf("a non-compiling candidate must be refused; got ok (%s)", reason)
	}

	// A candidate with no acceptance suite is refused (behavior must be pinned).
	if _, ok, _, _, reason := PromotePrimitive(ctx, execution.SysDictWAT, &evolution.AcceptanceSuite{}, p, sw); ok {
		t.Fatalf("a primitive with no acceptance must be refused; got ok (%s)", reason)
	}
}

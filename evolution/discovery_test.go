package evolution

import (
	"context"
	"testing"

	"github.com/mdcfrancis/flow/compiler"
)

// branchyDiscoveryCell mirrors cells/branchy.wat: byte 0 traps, low/high bytes
// enter distinct functions, and the returned byte is a varying scalar.
const branchyDiscoveryCell = `(module
  (memory (export "mem") 2)
  (func $low (result i32) i32.const 1 drop i32.const 0)
  (func $high (result i32) i32.const 2 drop i32.const 0)
  (func (export "run-tick") (param $p i32) (param $len i32) (result i32)
    (local $b i32)
    local.get $p i32.load8_u local.set $b
    local.get $b i32.eqz (if (then unreachable))
    local.get $b i32.const 128 i32.lt_u
    (if (then call $low drop) (else call $high drop))
    local.get $b))`

func TestCompactorDiscoveryInvariants(t *testing.T) {
	ctx := context.Background()
	art, err := compiler.NewCompilerService().CompileGenotype(branchyDiscoveryCell)
	if err != nil || !art.SyntaxPassed {
		t.Fatalf("compile branchy: %v (%s)", err, art.ErrorContext)
	}

	c := NewCompactor("run-tick", DefaultPayloadOffset, DefaultStateWindow, nil)
	var fault, branch, boundary, kept, considered int

	// Feed a spread of leading bytes; many should be compacted away.
	for i := 0; i < 40; i++ {
		b := byte((i * 37) % 256)
		var seed [16]byte
		rc, keep, err := c.Consider(ctx, art.Bytecode, []byte{b}, uint64(i+1), seed)
		if err != nil {
			t.Fatalf("consider %d: %v", i, err)
		}
		considered++
		if !keep {
			continue
		}
		kept++
		if rc.Discovery.FaultMitigation {
			fault++
		}
		if rc.Discovery.CodeBranch {
			branch++
		}
		if rc.Discovery.BoundaryExtremum {
			boundary++
		}
	}

	if kept >= considered {
		t.Fatalf("no compaction: kept %d of %d considered", kept, considered)
	}
	if fault == 0 {
		t.Error("expected at least one Fault Mitigation Asset (byte 0)")
	}
	if branch < 2 {
		t.Errorf("expected both branches discovered as code-branch coverage, got %d", branch)
	}
	if boundary == 0 {
		t.Error("expected boundary extrema to fire")
	}
	t.Logf("compaction: kept %d/%d (fault=%d branch=%d boundary=%d)", kept, considered, fault, branch, boundary)
}

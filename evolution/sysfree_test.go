package evolution

import (
	"context"
	"fmt"
	"testing"

	"github.com/mdcfrancis/flow/compiler"
)

// TestSysDispatchRefunded: dispatching to a sys:* primitive is refunded (essentially free
// shared infrastructure), so a cell dispatching to sys:* spends far less fuel than an identical
// cell dispatching to a non-sys cell doing the same work.
func TestSysDispatchRefunded(t *testing.T) {
	cs := compiler.NewCompilerService()
	echo := compileTraj(t, cs, `(module
  (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 100))
  (func (export "run-tick") (param $p i32) (param $l i32) (result i32) (i32.load (local.get $p))))`)

	caller := func(urn string) []byte {
		wat := `(module
  (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 100))
  (import "hdm:kernel/cell-dispatch" "invoke-cell" (func $invoke (param i32 i32 i32 i32) (result i32)))
  (data (i32.const 0x00020000) "` + urn + `")
  (func (export "run-tick") (param $p i32) (param $len i32) (result i32)
    (local $i i32) (local $acc i32)
    (local.set $i (i32.const 0)) (local.set $acc (i32.const 0))
    (block $d (loop $l
      (br_if $d (i32.ge_u (local.get $i) (i32.const 3)))
      (i32.store (i32.const 0x00030000) (local.get $i))
      (local.set $acc (i32.add (local.get $acc)
        (call $invoke (i32.const 0x00020000) (i32.const ` + fmt.Sprintf("%d", len(urn)) + `) (i32.const 0x00030000) (i32.const 4))))
      (local.set $i (i32.add (local.get $i) (i32.const 1)))
      (br $l)))
    (local.get $acc)))`
		return compileTraj(t, cs, wat)
	}
	sysCaller := caller("urn:hdm:sys:echo")
	demoCaller := caller("urn:hdm:demo:echo")
	resolver := func(urn string) ([]byte, bool) {
		if urn == "urn:hdm:sys:echo" || urn == "urn:hdm:demo:echo" {
			return echo, true
		}
		return nil, false
	}
	env := replayEnv{payloadOffset: DefaultPayloadOffset, stateWindow: DefaultStateWindow, resolver: resolver}
	inputs := [][]byte{{0, 0, 0, 0}}

	_, sysFuel, err := execTrajectory(context.Background(), sysCaller, EntryPoint, env, inputs)
	if err != nil {
		t.Fatalf("sys caller: %v", err)
	}
	_, demoFuel, err := execTrajectory(context.Background(), demoCaller, EntryPoint, env, inputs)
	if err != nil {
		t.Fatalf("demo caller: %v", err)
	}
	if sysFuel >= demoFuel {
		t.Fatalf("sys:* dispatch must be refunded (cheaper than a non-sys dispatch doing the same): sys=%d demo=%d", sysFuel, demoFuel)
	}
	t.Logf("sys-dispatch fuel=%d vs non-sys fuel=%d (refunded)", sysFuel, demoFuel)
}

package evolution

import (
	"context"
	"testing"

	"github.com/mdcfrancis/flow/compiler"
)

// dynRenderer draws one rect whose x = player_x (read from 0xB0000).
const dynRenderer = `(module
  (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 100))
  (func (export "render-frame") (param $base i32) (param $cap i32) (result i32)
    (i32.store (local.get $base) (i32.const 257))
    (i32.store (i32.add (local.get $base) (i32.const 4)) (i32.load (i32.const 720896)))
    (i32.store (i32.add (local.get $base) (i32.const 8)) (i32.const 100))
    (i32.store (i32.add (local.get $base) (i32.const 12)) (i32.const 8))
    (i32.store (i32.add (local.get $base) (i32.const 16)) (i32.const 8))
    (i32.store (i32.add (local.get $base) (i32.const 20)) (i32.const 65535))
    (i32.const 24)))`

// staticRenderer always draws the rect at x=160 (ignores player_x).
const staticRenderer = `(module
  (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 100))
  (func (export "render-frame") (param $base i32) (param $cap i32) (result i32)
    (i32.store (local.get $base) (i32.const 257))
    (i32.store (i32.add (local.get $base) (i32.const 4)) (i32.const 160))
    (i32.store (i32.add (local.get $base) (i32.const 8)) (i32.const 100))
    (i32.store (i32.add (local.get $base) (i32.const 12)) (i32.const 8))
    (i32.store (i32.add (local.get $base) (i32.const 16)) (i32.const 8))
    (i32.store (i32.add (local.get $base) (i32.const 20)) (i32.const 65535))
    (i32.const 24)))`

func TestDrawNearXForcesPositionTracking(t *testing.T) {
	x := 250
	sc := Scenario{Name: "player_at_250", Entry: "render-frame",
		Seed:   []SeedWrite{{At: "0xB0000", U32: []uint32{250}}},
		Expect: ScenarioExpect{Draw: &DrawExpect{Op: "rect", NearX: &x}}}
	score := func(wat string) int {
		art, err := compiler.NewCompilerService().CompileGenotype(wat)
		if err != nil || !art.SyntaxPassed {
			t.Fatalf("compile: %v", err)
		}
		p, _ := ScenarioScore(context.Background(), art.Bytecode, []Scenario{sc}, DefaultPayloadOffset, DefaultStateWindow, nil)
		return p
	}
	if score(staticRenderer) != 0 {
		t.Error("static renderer (x=160) must FAIL nearX=250")
	}
	if score(dynRenderer) != 1 {
		t.Error("renderer that draws at player_x must PASS nearX=250")
	}
}

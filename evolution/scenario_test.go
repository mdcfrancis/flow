package evolution

import (
	"context"
	"testing"

	"github.com/mdcfrancis/flow/compiler"
)

// moverCell is a minimal UI cell: it keeps a player x at 0xB0000 (sandbox) and,
// when the HMI buttons mask (0x50008) has bit0 set, advances x by 4 each frame,
// then emits one layer-1 rect at (x,100). It reads a monadic interface (the
// input register) and its output is observable only via the draw stream — the
// exact shape the scenario harness must be able to test.
const moverCell = `(module
  (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 100))
  (func (export "render-frame") (param $base i32) (param $cap i32) (result i32)
    (local $x i32)
    i32.const 720896 i32.load local.set $x           ;; 0xB0000 player x
    i32.const 327688 i32.load i32.const 1 i32.and      ;; 0x50008 buttons bit0
    if
      local.get $x i32.const 4 i32.add local.set $x
      i32.const 720896 local.get $x i32.store
    end
    local.get $base i32.const 257 i32.store            ;; op = layer1 | rect
    local.get $base i32.const 4 i32.add local.get $x i32.store
    local.get $base i32.const 8 i32.add i32.const 100 i32.store
    local.get $base i32.const 12 i32.add i32.const 8 i32.store
    local.get $base i32.const 16 i32.add i32.const 8 i32.store
    local.get $base i32.const 20 i32.add i32.const 16711935 i32.store
    i32.const 24))`

func compileTest(t *testing.T, wat string) []byte {
	t.Helper()
	art, err := compiler.NewCompilerService().CompileGenotype(wat)
	if err != nil || !art.SyntaxPassed {
		t.Fatalf("compile: %v (%s)", err, art.ErrorContext)
	}
	return art.Bytecode
}

func TestScenarioInputMovesSprite(t *testing.T) {
	bc := compileTest(t, moverCell)
	l1 := 1

	// With the button held, the layer-1 rect must move right over two frames.
	pressed := []Scenario{{
		Name:  "held button moves player right",
		Seed:  []SeedWrite{{At: "0x50008", U32: []uint32{1}}},
		Steps: 2, Entry: "render-frame",
		Expect: ScenarioExpect{Draw: &DrawExpect{Layer: &l1, Op: "rect", MinRecords: 1, Moved: "right"}},
	}}
	if p, total := ScenarioScore(context.Background(), bc, pressed, DefaultPayloadOffset, DefaultStateWindow, nil); p != 1 || total != 1 {
		t.Fatalf("pressed: passed %d/%d, want 1/1", p, total)
	}

	// With no input, the sprite must NOT move — the same scenario should fail,
	// proving the matcher isn't trivially satisfied.
	still := []Scenario{{
		Name:  "no input, no movement",
		Steps: 2, Entry: "render-frame",
		Expect: ScenarioExpect{Draw: &DrawExpect{Layer: &l1, Moved: "right"}},
	}}
	if p, _ := ScenarioScore(context.Background(), bc, still, DefaultPayloadOffset, DefaultStateWindow, nil); p != 0 {
		t.Fatalf("still: passed %d, want 0 (sprite should not move without input)", p)
	}

	// minRecords alone holds regardless of input (the cell always draws its rect).
	draws := []Scenario{{
		Name: "draws its scene", Steps: 1, Entry: "render-frame",
		Expect: ScenarioExpect{Draw: &DrawExpect{MinRecords: 1}},
	}}
	if p, _ := ScenarioScore(context.Background(), bc, draws, DefaultPayloadOffset, DefaultStateWindow, nil); p != 1 {
		t.Fatalf("draws: passed %d, want 1", p)
	}
}

// constCell returns a fixed value from run-tick, exercising the scalar-result
// scenario path (the compute-cell case).
const constCell = `(module
  (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 100))
  (func (export "run-tick") (param i32 i32) (result i32) i32.const 42))`

func TestScenarioScalarResult(t *testing.T) {
	bc := compileTest(t, constCell)
	want := int32(42)
	sc := []Scenario{{Name: "returns 42", Expect: ScenarioExpect{Result: &want}}}
	if p, total := ScenarioScore(context.Background(), bc, sc, DefaultPayloadOffset, DefaultStateWindow, nil); p != 1 || total != 1 {
		t.Fatalf("scalar: passed %d/%d, want 1/1", p, total)
	}
	bad := int32(7)
	sc2 := []Scenario{{Name: "not 7", Expect: ScenarioExpect{Result: &bad}}}
	if p, _ := ScenarioScore(context.Background(), bc, sc2, DefaultPayloadOffset, DefaultStateWindow, nil); p != 0 {
		t.Fatalf("scalar mismatch: passed %d, want 0", p)
	}
}

package execution

import (
	"context"
	"encoding/binary"
	"math"
	"path/filepath"
	"testing"

	"github.com/mdcfrancis/flow/compiler"
	"github.com/mdcfrancis/flow/inference"
	"github.com/mdcfrancis/flow/macro"
	"github.com/mdcfrancis/flow/storage"
)

// A macro-WAT FLOAT gravity cell, run for real: the orbiter starts at rest left of the
// well; after ticks its velocity and position must move TOWARD the well (gravity curves
// it) — the exact thing integer fixed-point underflowed to zero on.
func TestMacroFloatGravityCurves(t *testing.T) {
	ctx := context.Background()
	le, _ := storage.NewLedgerEngine(filepath.Join(t.TempDir(), "hdm.db"))
	defer le.Close()
	rm, err := NewRuntimeManager(ctx, le, inference.NewLocalModelClient("http://127.0.0.1:0", "test"))
	if err != nil {
		t.Fatalf("runtime: %v", err)
	}
	defer rm.Close(ctx)

	const wellOff, xOff, vxOff = 0xB0000, 0xB0010, 0xB0018
	fields := map[string]macro.Field{
		"well_x":     {Offset: wellOff, Float: true},
		"orbiter_x":  {Offset: xOff, Float: true},
		"orbiter_vx": {Offset: vxOff, Float: true},
	}
	src := `(cell run-tick
  (set orbiter_vx (f32.add (get orbiter_vx)
      (f32.div (f32.sub (get well_x) (get orbiter_x)) (f32.const 100.0))))
  (set orbiter_x (f32.add (get orbiter_x) (get orbiter_vx)))
  (i32.const 0))`
	wat, err := macro.Expand(src, fields)
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	art, cerr := compiler.NewCompilerService().CompileGenotype(wat)
	if cerr != nil || !art.SyntaxPassed {
		t.Fatalf("assemble: %v", cerr)
	}
	if err := rm.LoadCell("urn:hdm:test:grav", art.Bytecode); err != nil {
		t.Fatal(err)
	}
	wf := func(off uint32, v float32) {
		var b [4]byte
		binary.LittleEndian.PutUint32(b[:], math.Float32bits(v))
		rm.sharedMem.Write(off, b[:])
	}
	rf := func(off uint32) float32 {
		b, _ := rm.sharedMem.Read(off, 4)
		return math.Float32frombits(binary.LittleEndian.Uint32(b))
	}
	wf(wellOff, 300)
	wf(xOff, 100)
	wf(vxOff, 0)
	for i := 0; i < 5; i++ {
		if _, _, _, err := rm.ExecuteTrampoline("h", "urn:hdm:test:grav", "run-tick", 0, 0); err != nil {
			t.Fatalf("tick %d: %v", i, err)
		}
	}
	vx, x := rf(vxOff), rf(xOff)
	t.Logf("after 5 ticks: orbiter_x=%.3f orbiter_vx=%.4f (well at 300)", x, vx)
	if vx <= 0 {
		t.Errorf("gravity should pull RIGHT toward the well: vx=%.4f, want > 0", vx)
	}
	if x <= 100 {
		t.Errorf("orbiter should have moved toward the well: x=%.3f, want > 100", x)
	}
}

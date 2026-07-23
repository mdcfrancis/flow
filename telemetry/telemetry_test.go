package telemetry

import (
	"context"
	"math"
	"testing"

	"github.com/mdcfrancis/flow/compiler"
	"github.com/tetratelabs/wazero"
)

func TestCalculateHamiltonian(t *testing.T) {
	// Pure friction, no saliency: H = α·L + β·F + γ·C.
	m := SystemMetrics{LatencyNS: 1_000_000, WasmFuel: 50_000, TokenMilliCents: 400, SaliencyScore: 0}
	want := Alpha*1_000_000 + Beta*50_000 + Gamma*400
	if got := CalculateHamiltonian(m); math.Abs(got-want) > 1e-9 {
		t.Errorf("H = %v; want %v", got, want)
	}

	// Saliency is a reward: raising it lowers energy.
	low := CalculateHamiltonian(SystemMetrics{SaliencyScore: 10})
	high := CalculateHamiltonian(SystemMetrics{SaliencyScore: 1})
	if !(low < high) {
		t.Errorf("higher saliency should lower H: low=%v high=%v", low, high)
	}
}

func TestFuelAccounting(t *testing.T) {
	// A module whose exported entry calls a helper: exercising it must book
	// fuel at each function boundary the listener observes.
	wat := `(module
	  (func $helper (param i32) (result i32) local.get 0 i32.const 1 i32.add)
	  (func (export "run") (param i32) (result i32)
	    local.get 0 call $helper call $helper))`
	bc, err := compiler.NewCompilerService().CompileGenotype(wat)
	if err != nil || !bc.SyntaxPassed {
		t.Fatalf("compile: %v", err)
	}

	tracker := &FuelTracker{}
	ctx := Instrument(context.Background(), tracker)

	r := wazero.NewRuntime(ctx)
	defer r.Close(ctx)
	mod, err := r.Instantiate(ctx, bc.Bytecode)
	if err != nil {
		t.Fatalf("instantiate: %v", err)
	}
	if _, err := mod.ExportedFunction("run").Call(ctx, 10); err != nil {
		t.Fatalf("call: %v", err)
	}

	if tracker.Fuel() == 0 {
		t.Fatal("expected non-zero fuel after instrumented execution")
	}

	tracker.Reset()
	if tracker.Fuel() != 0 {
		t.Fatal("Reset did not zero the tracker")
	}
}

// TestCodeSizeReuseTerm: the code-size term makes OFFLOADING inline logic to a shared
// (dispatched) primitive a net win — the cell shrinks a lot while fuel rises a little — while a
// genuine fuel win (a cache eliminating dispatches) still dominates a small size increase.
func TestCodeSizeReuseTerm(t *testing.T) {
	// Large inline cell (low fuel) vs the same cell offloaded to a dispatched primitive
	// (small cell, +dispatch fuel). Offloading must lower H — this is what lets the system
	// prefer a shared abstraction over inlining.
	inline := CalculateHamiltonian(SystemMetrics{WasmFuel: 8, CodeBytes: 600})
	offloaded := CalculateHamiltonian(SystemMetrics{WasmFuel: 14, CodeBytes: 200})
	if offloaded >= inline {
		t.Fatalf("offloading to a shared primitive must lower H: inline=%.6f offloaded=%.6f", inline, offloaded)
	}

	// A real fuel win still dominates a size increase: a cache that adds code but eliminates
	// many dispatches must win over the larger-fuel, smaller-code baseline.
	cache := CalculateHamiltonian(SystemMetrics{WasmFuel: 236, CodeBytes: 500})
	baseline := CalculateHamiltonian(SystemMetrics{WasmFuel: 660, CodeBytes: 400})
	if cache >= baseline {
		t.Fatalf("a large fuel win must still dominate a small code increase: cache=%.6f baseline=%.6f", cache, baseline)
	}
}

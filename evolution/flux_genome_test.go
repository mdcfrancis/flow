package evolution

import (
	"context"
	"strings"
	"testing"

	"github.com/mdcfrancis/flow/flux"
)

// scriptedModel replays canned completions — a deterministic stand-in for a model
// that authors macro-WAT, so the operational sieve can be proven without a live model.
type scriptedModel struct {
	responses []string
	i         int
}

func (m *scriptedModel) InvokeReasoning(_ context.Context, _, _ string) (string, error) {
	r := m.responses[min(m.i, len(m.responses)-1)]
	m.i++
	return r, nil
}

func macroBallLayout() flux.Layout {
	return flux.Layout{
		"ball_x": {Type: flux.TInt, Offset: 0xB0000},
		"vel_x":  {Type: flux.TInt, Offset: 0xB0008},
	}
}

// macroPhysics integrates ball_x by vel_x each tick — the minimal compute cell.
const macroPhysics = `(cell run-tick (set ball_x (i32.add (get ball_x) (get vel_x))) (i32.const 0))`

// A macro-authored cell's GENOME is the macro-WAT source (raw WAT is a derived
// artifact), so the next solver frame refines it instead of restarting.
func TestSieveOutcomeGenomeIsMacro(t *testing.T) {
	model := &scriptedModel{responses: []string{
		"```\n" + macroPhysics + "\n```",
	}}
	out, err := RunSieveWithLayout(context.Background(), model, "sys", "seed", 5, macroBallLayout(), RunTickContract)
	if err != nil {
		t.Fatalf("sieve: %v", err)
	}
	if out.Flux == "" || !strings.Contains(out.Flux, "(cell run-tick") {
		t.Fatalf("macro source not captured on the outcome: %q", out.Flux)
	}
	// Genotype() persists the macro-WAT source, not the lowered raw WAT.
	if g := out.Genotype(); !strings.HasPrefix(strings.TrimSpace(g), "(cell") {
		t.Fatalf("Genotype() must be the macro-WAT program, got: %.40s", g)
	}
	if strings.Contains(out.Genotype(), "(module") {
		t.Fatal("Genotype() must not be expanded raw WAT for a macro cell")
	}
}

// entryContractFor reads a macro genome's shape from its entry/body (run-tick vs a
// render-frame/(scene …)), not from a raw WAT export name.
func TestEntryContractForMacroGenome(t *testing.T) {
	compute := `(cell run-tick (set ball_x (i32.add (get ball_x) (i32.const 1))) (i32.const 0))`
	view := `(cell render-frame (scene (circle (get ball_x) (get ball_y) (i32.const 8) (i32.const 0xFFCC33FF))))`
	if entryContractFor(compute) != RunTickContract {
		t.Fatal("a macro run-tick cell must map to run-tick")
	}
	if entryContractFor(view) != RenderFrameContract {
		t.Fatal("a macro (scene …) cell must map to render-frame")
	}
	// Raw WAT still works.
	if entryContractFor(`(module (func (export "render-frame")))`) != RenderFrameContract {
		t.Fatal("WAT render-frame detection regressed")
	}
}

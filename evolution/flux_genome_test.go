package evolution

import (
	"context"
	"strings"
	"testing"
)

// A Flux-authored cell's GENOME is the Flux source (WAT is a derived artifact),
// so the next solver frame refines it instead of restarting.
func TestSieveOutcomeGenomeIsFlux(t *testing.T) {
	model := &scriptedFlux{responses: []string{
		"```\n" + fluxPhysics + "\n```",
	}}
	out, err := RunSieveWithLayout(context.Background(), model, "sys", "seed", 5, fluxBallLayout(), RunTickContract)
	if err != nil {
		t.Fatalf("sieve: %v", err)
	}
	if out.Flux == "" || !strings.Contains(out.Flux, "(cell physics") {
		t.Fatalf("Flux source not captured on the outcome: %q", out.Flux)
	}
	// Genotype() persists the Flux, not the lowered WAT.
	if g := out.Genotype(); !strings.HasPrefix(strings.TrimSpace(g), "(cell") {
		t.Fatalf("Genotype() must be the Flux program, got: %.40s", g)
	}
	if strings.Contains(out.Genotype(), "(module") {
		t.Fatal("Genotype() must not be lowered WAT for a Flux cell")
	}
}

// entryContractFor reads a Flux genome's shape from its terminal (write vs draw),
// not from a WAT export name.
func TestEntryContractForFluxGenome(t *testing.T) {
	compute := `(cell physics (reads ball_x) (writes ball_x) (write (ball_x (+ ball_x 1))))`
	view := `(cell r (reads ball_x ball_y) (draw (circle ball_x ball_y 8 #xFFCC33FF)))`
	if entryContractFor(compute) != RunTickContract {
		t.Fatal("a Flux (write …) cell must map to run-tick")
	}
	if entryContractFor(view) != RenderFrameContract {
		t.Fatal("a Flux (draw …) cell must map to render-frame")
	}
	// WAT still works.
	if entryContractFor(`(module (func (export "render-frame")))`) != RenderFrameContract {
		t.Fatal("WAT render-frame detection regressed")
	}
}

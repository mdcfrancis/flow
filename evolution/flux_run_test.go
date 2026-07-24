package evolution

import (
	"strings"
	"testing"
)

// flux_run's engine: lower a Flux cell, run it in the real sandbox on chosen
// inputs, and report the resulting field values — the empirical feedback that
// lets an authoring model verify behavior instead of guessing.
func TestRunFluxCellReportsComputeOutputs(t *testing.T) {
	out, err := runFluxCell(fluxBallLayout(), fluxPhysics,
		`{"ball_x":100,"vel_x":5,"screen_w":320,"screen_h":240}`, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// After one tick, ball_x = 100 + 5 = 105; the summary reports it.
	if !strings.Contains(out, "ball_x=105") {
		t.Fatalf("expected ball_x=105 in output, got: %s", out)
	}
	if !strings.Contains(out, "vel_x=5") {
		t.Fatalf("expected vel_x unchanged at 5, got: %s", out)
	}
}

func TestRunFluxCellReportsBounce(t *testing.T) {
	out, err := runFluxCell(fluxBallLayout(), fluxPhysics,
		`{"ball_x":318,"vel_x":5,"screen_w":320,"screen_h":240}`, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// 318+5=323 crosses the right wall → vel_x flips to -5, ball_x clamps to 319.
	if !strings.Contains(out, "vel_x=-5") || !strings.Contains(out, "ball_x=319") {
		t.Fatalf("expected reflect (vel_x=-5, ball_x=319), got: %s", out)
	}
}

func TestRunFluxCellReportsDraw(t *testing.T) {
	layout := fluxBallLayout()
	out, err := runFluxCell(layout, fluxRenderer, `{"ball_x":250,"ball_y":120}`, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// The renderer draws a circle at (ball_x, ball_y).
	if !strings.Contains(out, "drew circle") || !strings.Contains(out, "a=250") {
		t.Fatalf("expected a circle at a=250, got: %s", out)
	}
}

// A cell that doesn't compile surfaces the Flux error (flux_run doubles as a check).
func TestRunFluxCellSurfacesFluxError(t *testing.T) {
	_, err := runFluxCell(fluxBallLayout(),
		`(cell x (reads ball_x) (writes ball_x) (write (ball_x (+ ball_x nope))))`, `{}`, 1)
	if err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("expected an unknown-name Flux error, got: %v", err)
	}
}

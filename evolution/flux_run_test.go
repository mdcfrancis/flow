package evolution

import (
	"strings"
	"testing"
)

// flux_run's engine: lower a Flux cell, run it in the real sandbox on chosen
// inputs, and report each writable field's per-tick TRAJECTORY — the empirical
// feedback that lets an authoring model see behavior over time (clamp vs bounce
// vs freeze), not just guess.
func TestRunFluxCellReportsComputeTrajectory(t *testing.T) {
	out, err := runFluxCell(fluxBallLayout(), fluxPhysics,
		`{"ball_x":100,"vel_x":5,"screen_w":320,"screen_h":240}`, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// One tick: ball_x 100 -> 105; the trajectory shows the path.
	if !strings.Contains(out, "ball_x: [100,105]") {
		t.Fatalf("expected ball_x trajectory [100,105], got: %s", out)
	}
}

// A multi-tick run makes a REFLECTION visible: ball_x rises, hits the wall, then
// reverses — the exact behavior a single-step run hides.
func TestRunFluxCellTrajectoryShowsReversal(t *testing.T) {
	out, err := runFluxCell(fluxBallLayout(), fluxPhysics,
		`{"ball_x":300,"vel_x":7,"screen_w":320,"screen_h":240}`, 8)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// Extract the ball_x sequence and assert it turns around (rises then falls).
	var line string
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "ball_x:") {
			line = l
		}
	}
	if line == "" || !strings.Contains(line, "319") {
		t.Fatalf("expected ball_x to reach the wall (319) in its trajectory, got: %s", out)
	}
	// After the wall the value must come back down (reversal), not stay pinned.
	if strings.Contains(line, "319,319,319") {
		t.Fatalf("ball_x clamped at the wall instead of reversing: %s", line)
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

package appgen

// The VISUAL acceptance path pairs with the vision engine: when the multimodal
// model judges a data-visualization renderer's output as not matching the objective
// (e.g. a Mandelbrot cell that draws one rectangle and passes only the trivial
// draw-floor), this authors a deterministic DRAW-COVERAGE check that the degenerate
// render fails and a real one passes — so vision decides WHETHER a renderer is
// deficient (sparingly), and a cheap check then drives evolution to fix it. Scoped
// to renderers that consume a grid ARRAY, so a simple sprite renderer is untouched.

import (
	"context"
	"fmt"

	"github.com/mdcfrancis/flow/evolution"
)

// AuthorVisualCoverage adds a draw-coverage check to a renderer that reads a grid
// array: seed the array with a spread of values and require the render to draw many
// records in many distinct colors — a flat/near-empty render fails, a data-driven
// one passes. Returns 1 if a check was added, 0 if the cell isn't an array
// visualizer or already carries the check.
func (g *Grower) AuthorVisualCoverage(ctx context.Context, cellURN string) (int, error) {
	ns := evolution.AppNamespaceOf(cellURN)
	c := evolution.LoadContract(g.ledger, ns)
	if c == nil {
		return 0, nil
	}
	// The largest array field is the grid/image the renderer must paint.
	arrOff, arrN := 0, 0
	for _, f := range c.Fields {
		if w := typeWords(f.Type); w > arrN {
			arrOff, arrN = f.Offset, w
		}
	}
	if arrN < 8 {
		return 0, nil // no grid to visualize — not a data renderer, leave it alone
	}
	suite, _ := evolution.LoadAcceptance(g.ledger, cellURN)
	if suite == nil {
		suite = &evolution.AcceptanceSuite{}
	}
	if hasScenarioNamed(suite, "visual_coverage") {
		return 0, nil
	}
	// Seed the first K cells with values spread across 0..250 so a value->color
	// renderer produces many distinct colors; the rest stay 0 (a uniform region).
	k := 48
	if k > arrN {
		k = arrN
	}
	ramp := make([]uint32, k)
	for i := range ramp {
		if k > 1 {
			ramp[i] = uint32(i * 250 / (k - 1))
		}
	}
	ct := evolution.LoadCheckThresholds(g.ledger) // evolvable, meta-acceptance-gated
	suite.Scenarios = append(suite.Scenarios, evolution.Scenario{
		Name: "visual_coverage", Entry: "render-frame", Steps: 1,
		Seed: []evolution.SeedWrite{{At: fmt.Sprintf("0x%X", arrOff), U32: ramp}},
		// The seeded cells run a value ramp, so a correct value→color renderer must
		// produce MANY distinct colors AND span dark→bright (a gradient with contrast).
		// The thresholds are an EVOLVABLE, meta-acceptance-gated artifact — they can only
		// get stricter (never weakened past the trust corpus).
		Expect: evolution.ScenarioExpect{Draw: &evolution.DrawExpect{
			MinRecords: 24, MinColors: ct.MinColors, MinBrightSpread: ct.MinBrightSpread,
		}},
	})
	if err := evolution.SaveAcceptance(g.ledger, cellURN, suite); err != nil {
		return 0, err
	}
	g.event("create", cellURN, "vision judged render deficient — authored a draw-coverage check")
	return 1, nil
}

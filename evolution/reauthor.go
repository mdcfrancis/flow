package evolution

import (
	"context"
	"fmt"
)

// REAUTHOR is the macro-tier work the language epoch gate injects: re-derive one
// cell under a new language version and verify it against its UNCHANGED scenarios
// (docs/language-evolution.md §2–3). It closes the gate's ReauthorFunc seam to the
// real orchestrator — build frames + scenario scoring — so a language change can be
// confirmed on the actual stack, not just planned.

// reauthorVerdict turns a scenario score into the (green, fitness) a rebuild reports:
// green iff every scenario passes; fitness is the pass fraction. Pure, so the verdict
// logic is unit-tested independently of the (model-driven) build loop.
func reauthorVerdict(passed, total int) (green bool, fitness float64) {
	if total <= 0 {
		return false, 0
	}
	return passed == total, float64(passed) / float64(total)
}

// ReauthorCell returns the ReauthorFunc the language epoch gate injects: it
// re-authors a stale cell against its unchanged scenarios by running build frames
// until the cell is green or progress stalls, then reports the committed genotype
// hash, greenness, and fitness. maxFrames bounds the model spend per cell. RunFrame
// commits an improving candidate, so a rejected epoch is undone by the gate's ref
// snapshot/rollback (ProposeLanguageChange), not here.
func (o *Orchestrator) ReauthorCell(ctx context.Context, maxFrames int) ReauthorFunc {
	if maxFrames < 1 {
		maxFrames = 1
	}
	return func(stale, _ Lineage) (string, bool, float64, error) {
		urn := stale.URN
		for i := 0; i < maxFrames; i++ {
			passed, total, err := o.ScoreCell(ctx, urn)
			if err != nil {
				return "", false, 0, err
			}
			if total > 0 && passed == total {
				break // already green — do not spend another frame
			}
			fr, err := o.RunFrame(ctx, urn)
			if err != nil {
				return "", false, 0, err
			}
			if fr.Transport {
				return "", false, 0, fmt.Errorf("reauthor %s: model unreachable", urn)
			}
			// No headway this frame (nothing committed and the candidate did not out-score
			// the baseline): stop spending — further frames on the same inputs won't help.
			if !fr.Committed && fr.AcceptCand <= fr.AcceptBase {
				break
			}
		}
		passed, total, err := o.ScoreCell(ctx, urn)
		if err != nil {
			return "", false, 0, err
		}
		green, fitness := reauthorVerdict(passed, total)
		desc, err := o.repo.Load(urn)
		if err != nil {
			return "", false, 0, err
		}
		return desc.GenotypeHash, green, fitness, nil
	}
}

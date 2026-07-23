package evolution

import (
	"context"
	"fmt"

	"github.com/mdcfrancis/flow/telemetry"
)

// Verdict is the adversarial critic's ruling on a candidate mutation.
type Verdict struct {
	Accepted              bool
	Reason                string
	OutputMatch           bool
	BaselineFuel          uint64
	CandidateFuel         uint64
	BaselineH             float64
	CandidateH            float64
	BaselineLatencyP99NS  uint64 // p99 guest execution latency (observability)
	CandidateLatencyP99NS uint64
	TapesRun              int // regression cases replayed (0 for arg-based replay)
	TapesMatched          int // cases the candidate reproduced bit-for-bit
}

// RunGauntlet plays a candidate cell against the production baseline inside
// shadow sandboxes: it runs both over the same input, performs a differential
// comparison of their outputs, and scores each with the Hamiltonian energy
// metric. A candidate is accepted only if it introduces no functional
// divergence AND does not raise system energy.
//
// Latency is intentionally excluded from the accept gate: wall-clock jitter
// would otherwise dominate the tiny fuel deltas that distinguish good
// mutations. Fuel, cognitive token cost, and saliency form the reproducible
// energy vector used to decide.
//
// minEnergyDrop is the minimum required reduction in Hamiltonian energy
// (baseline H − candidate H): a candidate is accepted only if it strictly
// lowers energy by more than this margin, so behavior-preserving-but-neutral
// rewrites do not churn the ledger. Pass 0 to require any positive drop.
func RunGauntlet(ctx context.Context, baseline, candidate []byte, entry string, args []uint64, tokenMilliCents uint32, saliency, minEnergyDrop float64) (*Verdict, error) {
	base, err := execCell(ctx, baseline, entry, args...)
	if err != nil {
		return nil, fmt.Errorf("baseline replay failed: %w", err)
	}

	cand, err := execCell(ctx, candidate, entry, args...)
	if err != nil {
		// A candidate that traps or panics is a hard functional regression.
		return &Verdict{
			Accepted:     false,
			Reason:       fmt.Sprintf("candidate faulted under replay: %v", err),
			BaselineFuel: base.Fuel,
		}, nil
	}

	match := equalU64(base.Results, cand.Results)
	hb := telemetry.CalculateHamiltonian(telemetry.SystemMetrics{
		WasmFuel: base.Fuel, TokenMilliCents: tokenMilliCents, SaliencyScore: saliency,
	})
	hc := telemetry.CalculateHamiltonian(telemetry.SystemMetrics{
		WasmFuel: cand.Fuel, TokenMilliCents: tokenMilliCents, SaliencyScore: saliency,
	})

	v := &Verdict{
		OutputMatch:   match,
		BaselineFuel:  base.Fuel,
		CandidateFuel: cand.Fuel,
		BaselineH:     hb,
		CandidateH:    hc,
	}
	improvement := hb - hc
	switch {
	case !match:
		v.Reason = "functional regression: candidate output diverged from baseline state"
	case improvement <= minEnergyDrop:
		v.Reason = fmt.Sprintf("insufficient energy reduction: ΔH=%.4f (need > %.4f)", improvement, minEnergyDrop)
	default:
		v.Accepted = true
		v.Reason = fmt.Sprintf("verified: behavior preserved and energy reduced by ΔH=%.4f", improvement)
	}
	return v, nil
}

func equalU64(a, b []uint64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

package evolution

import (
	"context"
	"fmt"
	"math"
	"sort"

	"github.com/mdcfrancis/flow/tapes"
	"github.com/mdcfrancis/flow/telemetry"
)

// RecordTape captures a regression tape by replaying an input payload through a
// (baseline) cell and recording the resulting output and state hashes as the
// expected bit-differential truth. This is how the production runtime seeds the
// critic's replay corpus.
func RecordTape(ctx context.Context, bytecode []byte, entry string, payloadOffset, stateWindow uint32, payload []byte, timestampNS uint64, entropy [16]byte) (*tapes.TransactionFrame, error) {
	env := replayEnv{
		payloadOffset: payloadOffset, payload: payload, stateWindow: stateWindow,
		clock: timestampNS, seed: entropy,
	}
	r, err := execReplay(ctx, bytecode, entry, env, uint64(payloadOffset), uint64(len(payload)))
	if err != nil {
		return nil, fmt.Errorf("record tape: %w", err)
	}
	return &tapes.TransactionFrame{
		MonadicTimestampNS: timestampNS,
		EntropySeed:        entropy,
		ExpectedOutputHash: r.OutputHash,
		ExpectedStateHash:  r.StateHash,
		InputPayload:       payload,
	}, nil
}

// RunGauntletTapes streams a regression-tape corpus through the candidate: each
// tape's input payload is memory-mapped and replayed, and the candidate must
// reproduce the tape's recorded output AND state hashes bit-for-bit (no
// behavioral divergence from verified production). Fuel is accumulated across
// every tape against a baseline replay of the same inputs, and the candidate is
// accepted only if it reproduces every tape and does not raise system energy.
func RunGauntletTapes(ctx context.Context, baseline, candidate []byte, entry string, corpus []*tapes.TransactionFrame, payloadOffset, stateWindow uint32, tokenMilliCents uint32, saliency, minEnergyDrop float64) (*Verdict, error) {
	cases := make([]RegressionCase, len(corpus))
	for i, f := range corpus {
		cases[i] = RegressionCase{Frame: f}
	}
	return RunGauntletCases(ctx, baseline, candidate, entry, cases, payloadOffset, stateWindow, tokenMilliCents, saliency, minEnergyDrop, nil)
}

// RunGauntletCases streams a regression corpus (normal + fault cases) through
// the candidate. Normal cases must reproduce the recorded output+state hashes
// bit-for-bit; fault cases must also trap (preserving a fault path). Fuel and
// latency are accumulated over the normal cases, and the candidate is accepted
// only if it reproduces every case AND lowers Hamiltonian energy by more than
// minEnergyDrop. Latency p99 is recorded for observability but excluded from the
// gate (wall-clock jitter would swamp fuel deltas).
func RunGauntletCases(ctx context.Context, baseline, candidate []byte, entry string, cases []RegressionCase, payloadOffset, stateWindow uint32, tokenMilliCents uint32, saliency, minEnergyDrop float64, resolver CellResolver) (*Verdict, error) {
	if len(cases) == 0 {
		return nil, fmt.Errorf("empty regression corpus")
	}

	v := &Verdict{OutputMatch: true, TapesRun: len(cases)}
	var baseFuel, candFuel uint64
	var baseLat, candLat []uint64

	for i, rc := range cases {
		tape := rc.Frame
		env := replayEnv{
			payloadOffset: payloadOffset, payload: tape.InputPayload, stateWindow: stateWindow,
			clock: tape.MonadicTimestampNS, seed: tape.EntropySeed, resolver: resolver,
		}
		args := []uint64{uint64(payloadOffset), uint64(len(tape.InputPayload))}

		if rc.ExpectFault {
			// The baseline faulted on this input; a behavior-preserving
			// candidate must fault too.
			if _, cerr := execReplay(ctx, candidate, entry, env, args...); cerr == nil {
				v.OutputMatch = false
				v.Reason = fmt.Sprintf("fault-path regression: candidate did not trap on fault case %d/%d", i+1, len(cases))
				return v, nil
			}
			v.TapesMatched++
			continue
		}

		cand, err := execReplay(ctx, candidate, entry, env, args...)
		if err != nil {
			v.OutputMatch = false
			v.Reason = fmt.Sprintf("candidate faulted on case %d/%d: %v", i+1, len(cases), err)
			v.BaselineFuel, v.CandidateFuel = baseFuel, candFuel
			return v, nil
		}
		if cand.OutputHash != tape.ExpectedOutputHash || cand.StateHash != tape.ExpectedStateHash {
			v.OutputMatch = false
			v.Reason = fmt.Sprintf("functional regression: candidate diverged from recorded case %d/%d", i+1, len(cases))
			v.CandidateFuel = candFuel
			return v, nil
		}
		candFuel += cand.Fuel
		candLat = append(candLat, cand.LatencyNS)
		v.TapesMatched++

		base, err := execReplay(ctx, baseline, entry, env, args...)
		if err != nil {
			return nil, fmt.Errorf("baseline faulted on case %d/%d: %w", i+1, len(cases), err)
		}
		baseFuel += base.Fuel
		baseLat = append(baseLat, base.LatencyNS)
	}

	v.BaselineFuel = baseFuel
	v.CandidateFuel = candFuel
	v.BaselineLatencyP99NS = percentile99(baseLat)
	v.CandidateLatencyP99NS = percentile99(candLat)
	v.BaselineH = telemetry.CalculateHamiltonian(telemetry.SystemMetrics{
		WasmFuel: baseFuel, TokenMilliCents: tokenMilliCents, SaliencyScore: saliency, CodeBytes: uint64(len(baseline)),
	})
	v.CandidateH = telemetry.CalculateHamiltonian(telemetry.SystemMetrics{
		WasmFuel: candFuel, TokenMilliCents: tokenMilliCents, SaliencyScore: saliency, CodeBytes: uint64(len(candidate)),
	})

	improvement := v.BaselineH - v.CandidateH
	if improvement <= minEnergyDrop {
		v.Reason = fmt.Sprintf("insufficient energy reduction across %d cases: ΔH=%.4f (need > %.4f)", len(cases), improvement, minEnergyDrop)
		return v, nil
	}
	v.Accepted = true
	v.Reason = fmt.Sprintf("verified across %d/%d cases: behavior preserved, energy reduced by ΔH=%.4f", v.TapesMatched, len(cases), improvement)
	return v, nil
}

// structuralTrajectoryRepeats is how many times the regression corpus is replayed as one
// shared-memory sequence, giving inputs the LOCALITY (recurrence) that makes a data structure
// pay off — a cache/index built on the first pass is read cheaply on later passes.
const structuralTrajectoryRepeats = 3

// trajectoryFrom builds a shared-memory input SEQUENCE from a regression corpus by replaying
// each (non-fault) input `repeats` times CONSECUTIVELY — modeling temporal locality (the same
// input recurs in a burst), the common case where caching pays off. Consecutive repetition
// means even a single-entry cache is rewarded, so the smallest useful data-structure refactor
// (a memo) already registers a win. Fault cases are excluded (amortized cost, normal path).
func trajectoryFrom(cases []RegressionCase, repeats int) [][]byte {
	if repeats < 1 {
		repeats = 1
	}
	var inputs [][]byte
	for _, rc := range cases {
		if rc.ExpectFault || rc.Frame == nil {
			continue
		}
		for r := 0; r < repeats; r++ {
			inputs = append(inputs, rc.Frame.InputPayload)
		}
	}
	return inputs
}

// RunTrajectoryGauntlet replays an input SEQUENCE through baseline and candidate on shared
// memory (execTrajectory), so an AMORTIZED data-structure win registers. The baseline defines
// the correct per-step outputs (the oracle); the candidate must reproduce them AND spend less
// TOTAL fuel over the trajectory. Unlike the per-case gauntlet it does not hash memory state —
// a structural refactor legitimately holds different memory in its private page; behavior is
// judged only by the per-step outputs. Used only after the per-case gauntlet has already
// confirmed behavior is preserved (OutputMatch), so this is purely the cost re-measure.
func RunTrajectoryGauntlet(ctx context.Context, baseline, candidate []byte, entry string, inputs [][]byte, tokenMilliCents uint32, saliency, minEnergyDrop float64, payloadOffset, stateWindow uint32, resolver CellResolver) (*Verdict, error) {
	if len(inputs) == 0 {
		return nil, fmt.Errorf("empty trajectory")
	}
	env := replayEnv{payloadOffset: payloadOffset, stateWindow: stateWindow, resolver: resolver}
	baseOut, baseFuel, err := execTrajectory(ctx, baseline, entry, env, inputs)
	if err != nil {
		return nil, fmt.Errorf("baseline trajectory: %w", err)
	}
	v := &Verdict{OutputMatch: true, TapesRun: len(inputs), BaselineFuel: baseFuel}
	candOut, candFuel, cerr := execTrajectory(ctx, candidate, entry, env, inputs)
	if cerr != nil {
		v.OutputMatch = false
		v.Reason = "candidate trajectory trapped: " + cerr.Error()
		return v, nil
	}
	v.CandidateFuel = candFuel
	for i := range baseOut {
		if i >= len(candOut) || candOut[i] != baseOut[i] {
			v.OutputMatch = false
			v.Reason = fmt.Sprintf("trajectory divergence at step %d/%d", i+1, len(inputs))
			return v, nil
		}
	}
	v.TapesMatched = len(inputs)
	v.BaselineH = telemetry.CalculateHamiltonian(telemetry.SystemMetrics{
		WasmFuel: baseFuel, TokenMilliCents: tokenMilliCents, SaliencyScore: saliency, CodeBytes: uint64(len(baseline)),
	})
	v.CandidateH = telemetry.CalculateHamiltonian(telemetry.SystemMetrics{
		WasmFuel: candFuel, TokenMilliCents: tokenMilliCents, SaliencyScore: saliency, CodeBytes: uint64(len(candidate)),
	})
	improvement := v.BaselineH - v.CandidateH
	if improvement <= minEnergyDrop {
		v.Reason = fmt.Sprintf("insufficient TOTAL energy reduction over %d-step trajectory: ΔH=%.4f (need > %.4f)", len(inputs), improvement, minEnergyDrop)
		return v, nil
	}
	v.Accepted = true
	v.Reason = fmt.Sprintf("trajectory verified over %d steps: behavior preserved, amortized energy reduced by ΔH=%.4f", len(inputs), improvement)
	return v, nil
}

// percentile99 returns the p99 of the samples (max for small n).
func percentile99(samples []uint64) uint64 {
	if len(samples) == 0 {
		return 0
	}
	s := append([]uint64(nil), samples...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	idx := int(math.Ceil(0.99*float64(len(s)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(s) {
		idx = len(s) - 1
	}
	return s[idx]
}

package evolution

import (
	"context"
	"fmt"
)

// SurvivesChaos replays the candidate over the recorded corpus under an
// adversarial ChaosProfile (clock drift, clamped memory, dropped signals) and
// reports whether it still reproduces every case without trapping — the
// internal-recovery requirement. A candidate whose behavior
// depends on the perturbed non-determinism (or that traps under stress) does not
// survive and must not be authorized for a hot-swap.
func SurvivesChaos(ctx context.Context, candidate []byte, entry string, cases []RegressionCase, payloadOffset, stateWindow uint32, chaos ChaosProfile, resolver CellResolver) (bool, string) {
	for i, rc := range cases {
		tape := rc.Frame
		env := replayEnv{
			payloadOffset: payloadOffset, payload: tape.InputPayload, stateWindow: stateWindow,
			clock: tape.MonadicTimestampNS, seed: tape.EntropySeed, resolver: resolver, chaos: chaos,
		}
		args := []uint64{uint64(payloadOffset), uint64(len(tape.InputPayload))}
		res, err := execReplay(ctx, candidate, entry, env, args...)

		if rc.ExpectFault {
			// A recorded fault path must remain a fault under stress.
			if err == nil {
				return false, fmt.Sprintf("case %d/%d stopped trapping under chaos", i+1, len(cases))
			}
			continue
		}
		if err != nil {
			return false, fmt.Sprintf("candidate trapped under chaos on case %d/%d: %v", i+1, len(cases), err)
		}
		if res.OutputHash != tape.ExpectedOutputHash || res.StateHash != tape.ExpectedStateHash {
			return false, fmt.Sprintf("candidate diverged under chaos on case %d/%d (fragile to perturbation)", i+1, len(cases))
		}
	}
	return true, "survived chaos"
}

package evolution

import (
	"context"

	"github.com/mdcfrancis/flow/tapes"
)

// RegressionCase is one entry in the critic's replay corpus: a recorded tape
// plus whether the baseline faulted on that input (a preserved fault path).
type RegressionCase struct {
	Frame       *tapes.TransactionFrame
	ExpectFault bool
	Discovery   DiscoveryReason
}

// DiscoveryReason records which Discovery Invariant(s) justified keeping a
// transaction in the permanent regression tape.
type DiscoveryReason struct {
	CodeBranch       bool // traversed a previously-unmapped function/branch
	BoundaryExtremum bool // registered a new min/max scalar
	FaultMitigation  bool // triggered a trap / recovery path
}

// Any reports whether at least one invariant fired.
func (d DiscoveryReason) Any() bool {
	return d.CodeBranch || d.BoundaryExtremum || d.FaultMitigation
}

// Compactor implements the Entropy Discovery & Compaction Engine: it admits a
// candidate transaction into the regression corpus only if replaying it against
// the baseline reveals new behavior — new code coverage, a new scalar extremum,
// or a fault path — preventing a storage explosion of redundant transactions.
type Compactor struct {
	entry         string
	payloadOffset uint32
	stateWindow   uint32
	resolver      CellResolver // optional; for dispatching baselines

	covered              map[uint32]bool
	haveBounds           bool
	minResult, maxResult int64
	minInput, maxInput   int64
}

// NewCompactor builds a compactor for a given entry point and shared-memory
// geometry. resolver may be nil for cells that do not dispatch.
func NewCompactor(entry string, payloadOffset, stateWindow uint32, resolver CellResolver) *Compactor {
	return &Compactor{
		entry:         entry,
		payloadOffset: payloadOffset,
		stateWindow:   stateWindow,
		resolver:      resolver,
		covered:       map[uint32]bool{},
	}
}

// Consider replays one candidate input against the baseline and decides whether
// it satisfies a Discovery Invariant. If so it returns a RegressionCase (kept),
// updating the cumulative coverage/extrema state; otherwise it returns keep=false
// (the transaction is compacted away).
func (c *Compactor) Consider(ctx context.Context, baseline []byte, payload []byte, timestampNS uint64, entropy [16]byte) (RegressionCase, bool, error) {
	env := replayEnv{
		payloadOffset: c.payloadOffset, payload: payload, stateWindow: c.stateWindow,
		clock: timestampNS, seed: entropy, resolver: c.resolver,
	}
	args := []uint64{uint64(c.payloadOffset), uint64(len(payload))}
	res, err := execReplay(ctx, baseline, c.entry, env, args...)

	frame := &tapes.TransactionFrame{
		MonadicTimestampNS: timestampNS,
		EntropySeed:        entropy,
		InputPayload:       payload,
	}

	// Fault Mitigation Asset: the input drives the baseline into a trap.
	if err != nil {
		reason := DiscoveryReason{FaultMitigation: true}
		return RegressionCase{Frame: frame, ExpectFault: true, Discovery: reason}, true, nil
	}

	frame.ExpectedOutputHash = res.OutputHash
	frame.ExpectedStateHash = res.StateHash

	var reason DiscoveryReason

	// Code-Branch Exploration: entered a function not previously covered.
	for _, idx := range res.Coverage {
		if !c.covered[idx] {
			reason.CodeBranch = true
			c.covered[idx] = true
		}
	}

	// Boundary Extremum: a new min/max in the result or the input scalar.
	var resultScalar int64
	if len(res.Results) > 0 {
		resultScalar = int64(res.Results[0])
	}
	var inputScalar int64
	if len(payload) > 0 {
		inputScalar = int64(payload[0])
	}
	if !c.haveBounds {
		c.minResult, c.maxResult = resultScalar, resultScalar
		c.minInput, c.maxInput = inputScalar, inputScalar
		c.haveBounds = true
		reason.BoundaryExtremum = true
	} else {
		if resultScalar < c.minResult {
			c.minResult, reason.BoundaryExtremum = resultScalar, true
		}
		if resultScalar > c.maxResult {
			c.maxResult, reason.BoundaryExtremum = resultScalar, true
		}
		if inputScalar < c.minInput {
			c.minInput, reason.BoundaryExtremum = inputScalar, true
		}
		if inputScalar > c.maxInput {
			c.maxInput, reason.BoundaryExtremum = inputScalar, true
		}
	}

	if !reason.Any() {
		return RegressionCase{}, false, nil
	}
	return RegressionCase{Frame: frame, Discovery: reason}, true, nil
}

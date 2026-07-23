package evolution

// Acceptance suites are the Stage-4 "autonomous QA" artifact: model-authored
// test cases derived from a subsystem's semantic manifest (before correct code
// exists). Unlike regression tapes — which enforce "preserve current behavior"
// — an acceptance suite encodes the *target* behavior. A crude genesis cell
// fails them; the orchestrator drives evolution to pass more of them.

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"

	"github.com/mdcfrancis/flow/storage"
)

// AcceptanceTest is one spec-derived case: an integer input written to the
// inbound region and the expected i32 result.
type AcceptanceTest struct {
	Name     string `json:"name"`
	Input    int32  `json:"input"`
	Expected int32  `json:"expected"`
}

// AcceptanceSuite is the persisted set of acceptance checks for a cell: scalar
// Tests (int-in/int-out, for compute cells) and/or behavioral Scenarios (seed
// memory → run → assert on the draw stream, for UI cells and stateful behavior).
type AcceptanceSuite struct {
	Tests     []AcceptanceTest `json:"tests,omitempty"`
	Scenarios []Scenario       `json:"scenarios,omitempty"`
}

func acceptanceRefURN(cell string) string { return cell + ":acceptance" }

// SaveAcceptance persists a cell's acceptance suite.
func SaveAcceptance(ledger *storage.LedgerEngine, cell string, suite *AcceptanceSuite) error {
	raw, err := json.Marshal(suite)
	if err != nil {
		return fmt.Errorf("encode acceptance suite: %w", err)
	}
	h, err := ledger.WriteBlock(raw)
	if err != nil {
		return fmt.Errorf("persist acceptance suite: %w", err)
	}
	return ledger.UpdateRef(acceptanceRefURN(cell), h)
}

// LoadAcceptance returns a cell's acceptance suite, or nil if none exists.
func LoadAcceptance(ledger *storage.LedgerEngine, cell string) (*AcceptanceSuite, error) {
	h, err := ledger.GetRef(acceptanceRefURN(cell))
	if err != nil {
		return nil, nil
	}
	raw, err := ledger.ReadBlock(h)
	if err != nil {
		return nil, fmt.Errorf("read acceptance suite: %w", err)
	}
	var suite AcceptanceSuite
	if err := json.Unmarshal(raw, &suite); err != nil {
		return nil, fmt.Errorf("decode acceptance suite: %w", err)
	}
	return &suite, nil
}

// AcceptanceScore runs each test's input through the phenotype and counts how
// many produce the expected result. A test whose execution traps counts as a
// failure.
func AcceptanceScore(ctx context.Context, phenotype []byte, entry string, suite *AcceptanceSuite, payloadOffset, stateWindow uint32, resolver CellResolver) (passed, total int) {
	if suite == nil {
		return 0, 0
	}
	total = len(suite.Tests)
	for _, tc := range suite.Tests {
		payload := make([]byte, 4)
		binary.LittleEndian.PutUint32(payload, uint32(tc.Input))
		env := replayEnv{
			payloadOffset: payloadOffset, payload: payload, stateWindow: stateWindow,
			resolver: resolver,
		}
		res, err := execReplay(ctx, phenotype, entry, env, uint64(payloadOffset), uint64(len(payload)))
		if err != nil || len(res.Results) == 0 {
			continue
		}
		if uint32(res.Results[0]) == uint32(tc.Expected) {
			passed++
		}
	}
	return passed, total
}

package evolution

import "context"

// Janitor is the stateless Tape Compaction worker. It
// replays a cell's stored regression tapes against the cell's current phenotype
// and, via the Discovery-Invariant compactor, keeps only the tapes that reveal
// distinct behavior — new code coverage, a new scalar extremum, or a fault path
// — dropping duplicate transaction envelopes that share an execution path and
// state-delta signature. This clamps the validation suite to a lean footprint.
type Janitor struct {
	store         *TapeStore
	entry         string
	payloadOffset uint32
	stateWindow   uint32
	resolver      CellResolver
}

// NewJanitor builds a compaction janitor for a given entry point and shared-
// memory geometry. resolver may be nil.
func NewJanitor(store *TapeStore, entry string, payloadOffset, stateWindow uint32, resolver CellResolver) *Janitor {
	return &Janitor{store: store, entry: entry, payloadOffset: payloadOffset, stateWindow: stateWindow, resolver: resolver}
}

// Compact loads the cell's stored tapes, reconsiders each against the phenotype,
// and rewrites the index to the retained set. Returns the counts before and
// after pruning.
func (j *Janitor) Compact(ctx context.Context, cellURN string, phenotype []byte) (before, after int, err error) {
	frames, err := j.store.Load(cellURN)
	if err != nil {
		return 0, 0, err
	}
	before = len(frames)
	if before == 0 {
		return 0, 0, nil
	}

	comp := NewCompactor(j.entry, j.payloadOffset, j.stateWindow, j.resolver)
	kept := frames[:0:0]
	for _, f := range frames {
		rc, keep, cerr := comp.Consider(ctx, phenotype, f.InputPayload, f.MonadicTimestampNS, f.EntropySeed)
		if cerr != nil {
			return before, 0, cerr
		}
		if keep {
			kept = append(kept, rc.Frame)
		}
	}
	if err := j.store.Replace(cellURN, kept); err != nil {
		return before, 0, err
	}
	return before, len(kept), nil
}

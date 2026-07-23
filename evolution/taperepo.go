package evolution

import (
	"encoding/json"
	"fmt"

	"github.com/mdcfrancis/flow/storage"
	"github.com/mdcfrancis/flow/tapes"
)

// TapeStore persists regression tapes per cell in the CAS ledger: each tape is
// a content-addressed block, and a per-cell index (itself a block) lists the
// tape hashes. The mutable reference urn:hdm:tapes:<cell> points at the index.
type TapeStore struct {
	ledger *storage.LedgerEngine
}

// NewTapeStore binds a tape store to a ledger.
func NewTapeStore(ledger *storage.LedgerEngine) *TapeStore {
	return &TapeStore{ledger: ledger}
}

func tapeIndexURN(cellURN string) string { return "urn:hdm:tapes:" + cellURN }

// loadIndex returns the list of tape block hashes for a cell (empty if none).
func (s *TapeStore) loadIndex(cellURN string) ([]string, error) {
	h, err := s.ledger.GetRef(tapeIndexURN(cellURN))
	if err != nil {
		return nil, nil // no index yet
	}
	raw, err := s.ledger.ReadBlock(h)
	if err != nil {
		return nil, fmt.Errorf("read tape index: %w", err)
	}
	var hashes []string
	if err := json.Unmarshal(raw, &hashes); err != nil {
		return nil, fmt.Errorf("decode tape index: %w", err)
	}
	return hashes, nil
}

func (s *TapeStore) saveIndex(cellURN string, hashes []string) error {
	raw, err := json.Marshal(hashes)
	if err != nil {
		return fmt.Errorf("encode tape index: %w", err)
	}
	idxHash, err := s.ledger.WriteBlock(raw)
	if err != nil {
		return fmt.Errorf("persist tape index: %w", err)
	}
	return s.ledger.UpdateRef(tapeIndexURN(cellURN), idxHash)
}

// Append persists the given frames as CAS blocks and adds them to the cell's
// tape index.
func (s *TapeStore) Append(cellURN string, frames []*tapes.TransactionFrame) error {
	if len(frames) == 0 {
		return nil
	}
	hashes, err := s.loadIndex(cellURN)
	if err != nil {
		return err
	}
	for _, f := range frames {
		h, err := s.ledger.WriteBlock(f.Marshal())
		if err != nil {
			return fmt.Errorf("persist tape: %w", err)
		}
		hashes = append(hashes, h)
	}
	return s.saveIndex(cellURN, hashes)
}

// Load reads all persisted tapes for a cell in index order.
func (s *TapeStore) Load(cellURN string) ([]*tapes.TransactionFrame, error) {
	hashes, err := s.loadIndex(cellURN)
	if err != nil {
		return nil, err
	}
	frames := make([]*tapes.TransactionFrame, 0, len(hashes))
	for _, h := range hashes {
		raw, err := s.ledger.ReadBlock(h)
		if err != nil {
			return nil, fmt.Errorf("read tape %s: %w", h, err)
		}
		f, err := tapes.Unmarshal(raw)
		if err != nil {
			return nil, fmt.Errorf("decode tape %s: %w", h, err)
		}
		frames = append(frames, f)
	}
	return frames, nil
}

// Count returns the number of tapes indexed for a cell.
func (s *TapeStore) Count(cellURN string) (int, error) {
	hashes, err := s.loadIndex(cellURN)
	return len(hashes), err
}

// Replace rewrites the cell's tape index to exactly the given frames (the CAS
// blocks are immutable and simply become unreferenced). Used by the janitor.
func (s *TapeStore) Replace(cellURN string, frames []*tapes.TransactionFrame) error {
	hashes := make([]string, 0, len(frames))
	for _, f := range frames {
		h, err := s.ledger.WriteBlock(f.Marshal())
		if err != nil {
			return fmt.Errorf("persist tape: %w", err)
		}
		hashes = append(hashes, h)
	}
	return s.saveIndex(cellURN, hashes)
}

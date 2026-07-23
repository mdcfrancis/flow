package codependency

// Package codependency tracks co-mutation: empirically, when optimizing one cell
// causes a regression in another, it bundles the tightly-coupled cells into a
// co-dependent evolutionary batch once their observed coupling crosses a
// threshold.
//
//	Gravity(X, Y) = joint failures(X→Y) / isolation pass attempts(X)

import (
	"encoding/json"
	"fmt"

	"github.com/mdcfrancis/flow/storage"
)

// MapURN is the mutable ledger reference holding the gravity matrix block.
const MapURN = "urn:hdm:sys:codependency:map"

// Matrix is the persisted co-dependency state.
type Matrix struct {
	// Joint[x][y] counts times a mutation targeting x caused a regression in y.
	Joint map[string]map[string]int `json:"joint"`
	// Attempts[x] counts isolation optimization passes targeting x.
	Attempts map[string]int `json:"attempts"`
}

func newMatrix() *Matrix {
	return &Matrix{Joint: map[string]map[string]int{}, Attempts: map[string]int{}}
}

// Gravity returns the empirical coupling weight from x to y in [0,1].
func (m *Matrix) Gravity(x, y string) float64 {
	attempts := m.Attempts[x]
	if attempts == 0 {
		return 0
	}
	return float64(m.Joint[x][y]) / float64(attempts)
}

// Batch returns the cells that should co-evolve with x: x itself plus every y
// whose Gravity(x, y) meets or exceeds threshold.
func (m *Matrix) Batch(x string, threshold float64) []string {
	batch := []string{x}
	for y := range m.Joint[x] {
		if y != x && m.Gravity(x, y) >= threshold {
			batch = append(batch, y)
		}
	}
	return batch
}

// Tracker persists and updates the gravity matrix on the ledger.
type Tracker struct {
	ledger *storage.LedgerEngine
}

// NewTracker binds a tracker to a ledger.
func NewTracker(ledger *storage.LedgerEngine) *Tracker {
	return &Tracker{ledger: ledger}
}

// Load reads the current matrix, returning an empty one if none exists yet.
func (t *Tracker) Load() (*Matrix, error) {
	hash, err := t.ledger.GetRef(MapURN)
	if err != nil {
		return newMatrix(), nil
	}
	raw, err := t.ledger.ReadBlock(hash)
	if err != nil {
		return nil, fmt.Errorf("read gravity map: %w", err)
	}
	var m Matrix
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("decode gravity map: %w", err)
	}
	if m.Joint == nil {
		m.Joint = map[string]map[string]int{}
	}
	if m.Attempts == nil {
		m.Attempts = map[string]int{}
	}
	return &m, nil
}

func (t *Tracker) save(m *Matrix) error {
	payload, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("encode gravity map: %w", err)
	}
	hash, err := t.ledger.WriteBlock(payload)
	if err != nil {
		return fmt.Errorf("persist gravity map: %w", err)
	}
	return t.ledger.UpdateRef(MapURN, hash)
}

// RecordIsolationAttempt notes one optimization pass targeting x.
func (t *Tracker) RecordIsolationAttempt(x string) error {
	m, err := t.Load()
	if err != nil {
		return err
	}
	m.Attempts[x]++
	return t.save(m)
}

// RecordJointFailure notes that a mutation targeting x regressed y, and returns
// the updated gravity weight.
func (t *Tracker) RecordJointFailure(x, y string) (float64, error) {
	m, err := t.Load()
	if err != nil {
		return 0, err
	}
	if m.Joint[x] == nil {
		m.Joint[x] = map[string]int{}
	}
	m.Joint[x][y]++
	if err := t.save(m); err != nil {
		return 0, err
	}
	return m.Gravity(x, y), nil
}

// Batch loads the matrix and returns the co-dependent evolutionary batch for x.
func (t *Tracker) Batch(x string, threshold float64) ([]string, error) {
	m, err := t.Load()
	if err != nil {
		return nil, err
	}
	return m.Batch(x, threshold), nil
}

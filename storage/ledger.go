package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"go.etcd.io/bbolt"
)

type LedgerEngine struct {
	db *bbolt.DB
}

var (
	bucketBlocks = []byte("hdm_blocks")
	bucketRefs   = []byte("hdm_refs")
)

func NewLedgerEngine(dbPath string) (*LedgerEngine, error) {
	db, err := bbolt.Open(dbPath, 0600, &bbolt.Options{})
	if err != nil {
		return nil, fmt.Errorf("failed to open bbolt db: %w", err)
	}

	err = db.Update(func(tx *bbolt.Tx) error {
		if _, err := tx.CreateBucketIfNotExists(bucketBlocks); err != nil {
			return err
		}
		if _, err := tx.CreateBucketIfNotExists(bucketRefs); err != nil {
			return err
		}
		return nil
	})

	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("failed to initialize buckets: %w", err)
	}

	return &LedgerEngine{db: db}, nil
}

func (le *LedgerEngine) WriteBlock(payload []byte) (string, error) {
	hash := sha256.Sum256(payload)
	hashStr := hex.EncodeToString(hash[:])

	err := le.db.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket(bucketBlocks)
		return b.Put([]byte(hashStr), payload)
	})

	if err != nil {
		return "", fmt.Errorf("failed to write block: %w", err)
	}

	return hashStr, nil
}

func (le *LedgerEngine) ReadBlock(hashStr string) ([]byte, error) {
	var data []byte
	err := le.db.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket(bucketBlocks)
		v := b.Get([]byte(hashStr))
		if v == nil {
			return fmt.Errorf("block not found: %s", hashStr)
		}
		data = make([]byte, len(v))
		copy(data, v)
		return nil
	})

	if err != nil {
		return nil, err
	}

	return data, nil
}

func (le *LedgerEngine) UpdateRef(urn string, targetHash string) error {
	return le.db.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket(bucketRefs)
		return b.Put([]byte(urn), []byte(targetHash))
	})
}

// DeleteRefs removes mutable references by URN (e.g. retiring a whole app's
// cells + envelope so it does not rehydrate). Missing URNs are ignored. The
// underlying content blocks are left for the GC sweep to reclaim once unreachable.
func (le *LedgerEngine) DeleteRefs(urns ...string) error {
	return le.db.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket(bucketRefs)
		for _, urn := range urns {
			if err := b.Delete([]byte(urn)); err != nil {
				return err
			}
		}
		return nil
	})
}

// UpdateRefCAS performs Compare-And-Swap Optimistic Concurrency Control to update ref.
func (le *LedgerEngine) UpdateRefCAS(urn string, targetHash string, expectedHash string) (bool, error) {
	var ok bool
	err := le.db.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket(bucketRefs)
		current := string(b.Get([]byte(urn)))
		if current != expectedHash {
			return fmt.Errorf("optimistic concurrency check failed: expected %s, got %s", expectedHash, current)
		}
		ok = true
		return b.Put([]byte(urn), []byte(targetHash))
	})
	return ok, err
}

// CommitEvolution performs the atomic MVCC hot-swap: within a single write
// transaction it verifies the global manifest root still equals baseRoot
// (optimistic concurrency check), advances the target cell's reference pointer
// to targetHash, and advances the manifest root to nextRoot. If the root has
// shifted since baseRoot was observed, the whole commit aborts with no state
// contamination. Returns true on a successful hot-swap.
func (le *LedgerEngine) CommitEvolution(manifestURN, baseRoot, targetURN, targetHash, nextRoot string) (bool, error) {
	committed := false
	err := le.db.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket(bucketRefs)
		current := string(b.Get([]byte(manifestURN)))
		if current != baseRoot {
			return fmt.Errorf("MVCC_CONCURRENCY_CONFLICT: manifest root shifted from %q to %q", baseRoot, current)
		}
		if err := b.Put([]byte(targetURN), []byte(targetHash)); err != nil {
			return err
		}
		if err := b.Put([]byte(manifestURN), []byte(nextRoot)); err != nil {
			return err
		}
		committed = true
		return nil
	})
	return committed, err
}

func (le *LedgerEngine) GetRef(urn string) (string, error) {
	var hash string
	err := le.db.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket(bucketRefs)
		v := b.Get([]byte(urn))
		if v == nil {
			return fmt.Errorf("reference not found for urn: %s", urn)
		}
		hash = string(v)
		return nil
	})

	if err != nil {
		return "", err
	}

	return hash, nil
}

// Refs returns a snapshot of all mutable references (urn -> block hash).
func (le *LedgerEngine) Refs() (map[string]string, error) {
	out := map[string]string{}
	err := le.db.View(func(tx *bbolt.Tx) error {
		return tx.Bucket(bucketRefs).ForEach(func(k, v []byte) error {
			out[string(k)] = string(v)
			return nil
		})
	})
	return out, err
}

// Sweep deletes every block in the CAS bucket whose hash is not in the
// reachable set (mark-and-sweep garbage collection). It returns the number of
// blocks scanned and deleted. Because CAS blocks are immutable and content-
// addressed, deleting an unreferenced block is always safe.
func (le *LedgerEngine) Sweep(reachable map[string]bool) (scanned, deleted int, err error) {
	err = le.db.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket(bucketBlocks)
		var doomed [][]byte
		if err := b.ForEach(func(k, _ []byte) error {
			scanned++
			if !reachable[string(k)] {
				doomed = append(doomed, append([]byte(nil), k...))
			}
			return nil
		}); err != nil {
			return err
		}
		for _, k := range doomed {
			if err := b.Delete(k); err != nil {
				return err
			}
			deleted++
		}
		return nil
	})
	return scanned, deleted, err
}

func (le *LedgerEngine) Close() error {
	return le.db.Close()
}

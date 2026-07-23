package engine

// Package engine implements the evolutionary commit pipeline: an
// optimistic multi-version concurrency control (MVCC) coordinator that hot-
// swaps a mutated cell into the live ledger only if the global manifest root
// has not shifted during out-of-band model inference.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/mdcfrancis/flow/storage"
)

// ManifestRootURN is the mutable anchor pointing at the current global manifest
// content-addressed root. It is the single linearization point for evolution.
const ManifestRootURN = "urn:hdm:sys:manifest:root"

// MVCCCoordinator serializes speculative evolution commits against the live
// ledger.
type MVCCCoordinator struct {
	ledger *storage.LedgerEngine
}

// NewMVCCCoordinator binds a coordinator to a ledger.
func NewMVCCCoordinator(ledger *storage.LedgerEngine) *MVCCCoordinator {
	return &MVCCCoordinator{ledger: ledger}
}

// InitManifest seeds the manifest root anchor if it does not yet exist, and
// returns the current root.
func (mc *MVCCCoordinator) InitManifest() (string, error) {
	if root, err := mc.ledger.GetRef(ManifestRootURN); err == nil {
		return root, nil
	}
	genesis := manifestRoot("", "", "")
	if err := mc.ledger.UpdateRef(ManifestRootURN, genesis); err != nil {
		return "", fmt.Errorf("failed to seed manifest root: %w", err)
	}
	return genesis, nil
}

// ProposeEvolutionCommit attempts a zero-downtime hot-swap of targetURN to
// nextBytecodeHash. It aborts if the live manifest root has drifted away from
// baseRoot (the root observed when the mutation began), guaranteeing snapshot
// isolation between speculative evolution and production execution. On success
// it returns the new manifest root.
func (mc *MVCCCoordinator) ProposeEvolutionCommit(ctx context.Context, baseRoot, targetURN, nextBytecodeHash string) (string, error) {
	// Derive the next content-addressed manifest root from the base root and
	// the mutation being applied, so the anchor is a genuine Merkle linkage
	// rather than a static label.
	nextRoot := manifestRoot(baseRoot, targetURN, nextBytecodeHash)

	ok, err := mc.ledger.CommitEvolution(ManifestRootURN, baseRoot, targetURN, nextBytecodeHash, nextRoot)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("evolution commit did not apply")
	}
	return nextRoot, nil
}

// manifestRoot computes the content-addressed identifier of a manifest node
// linking a parent root to the mutation it applies.
func manifestRoot(parentRoot, targetURN, bytecodeHash string) string {
	h := sha256.New()
	h.Write([]byte(parentRoot))
	h.Write([]byte{0})
	h.Write([]byte(targetURN))
	h.Write([]byte{0})
	h.Write([]byte(bytecodeHash))
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

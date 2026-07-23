package gc

// Package gc implements mark-and-sweep garbage collection over the HDM
// content-addressable store. The janitor prunes the tape *index*, but the
// underlying immutable blocks remain; without collection the bbolt file grows
// without bound. Collect marks every block reachable from a mutable reference —
// following node descriptors to their genotype/phenotype blocks and tape
// indexes to their tape blocks — then sweeps the rest.

import (
	"encoding/json"

	"github.com/mdcfrancis/flow/manifest"
	"github.com/mdcfrancis/flow/storage"
)

// Collect performs one GC pass and returns the number of blocks scanned and
// deleted.
func Collect(ledger *storage.LedgerEngine) (scanned, deleted int, err error) {
	refs, err := ledger.Refs()
	if err != nil {
		return 0, 0, err
	}

	reachable := map[string]bool{}
	for _, hash := range refs {
		reachable[hash] = true
		raw, rerr := ledger.ReadBlock(hash)
		if rerr != nil {
			// Synthetic anchors (e.g. the manifest root) are not stored blocks.
			continue
		}
		// A node descriptor reaches its genotype and phenotype blocks.
		var desc manifest.NodeDescriptor
		if json.Unmarshal(raw, &desc) == nil && desc.GenotypeHash != "" && desc.PhenotypeHash != "" {
			reachable[desc.GenotypeHash] = true
			reachable[desc.PhenotypeHash] = true
		}
		// A tape index reaches each of its tape blocks.
		var idx []string
		if json.Unmarshal(raw, &idx) == nil {
			for _, h := range idx {
				reachable[h] = true
			}
		}
	}

	return ledger.Sweep(reachable)
}

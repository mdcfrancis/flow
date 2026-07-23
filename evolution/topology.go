package evolution

import (
	"fmt"

	"github.com/mdcfrancis/flow/codependency"
	"github.com/mdcfrancis/flow/manifest"
)

// TopologyMutationKind selects the structural transformation a frame applies.
type TopologyMutationKind int

const (
	MutateGenotypeOnly     TopologyMutationKind = iota // instruction-level tweaking
	ExecuteCellularFission                             // split one cell into sister peers
	ExecuteCellularFusion                              // merge coupled cells, drop the bridge
)

func (k TopologyMutationKind) String() string {
	switch k {
	case ExecuteCellularFission:
		return "fission"
	case ExecuteCellularFusion:
		return "fusion"
	default:
		return "genotype"
	}
}

// FissionPhenotypeBytes is the compiled-size threshold above which a cell is a
// Fission candidate.
const FissionPhenotypeBytes = 64 * 1024

// Default Fusion triggers.
const (
	DefaultGravityThreshold         = 0.5
	DefaultBridgeLatencyThresholdNS = 50_000
)

// TopologyPlan is the structural decision for a frame.
type TopologyPlan struct {
	Kind    TopologyMutationKind
	Target  string
	Partner string // set for fusion
	Reason  string
}

// PlanTopology decides the structural mutation for target:
//   - Fission if the compiled phenotype exceeds the size threshold, or the cell
//     spans multiple (non-overlapping) domain tags.
//   - Fusion if some partner shares a high co-mutation score with the target and
//     the recent bridge p99 latency is elevated (the bridge tax is worth removing).
//   - Otherwise ordinary genotype refinement.
func PlanTopology(target string, desc *manifest.NodeDescriptor, phenotypeSize int, gravity *codependency.Tracker, partners []string, lastBridgeLatencyP99NS uint64, gravityThreshold float64, latencyThresholdNS uint64) (TopologyPlan, error) {
	// Fission: bloated bytecode or multiple decoupled domains.
	if phenotypeSize > FissionPhenotypeBytes {
		return TopologyPlan{Kind: ExecuteCellularFission, Target: target,
			Reason: fmt.Sprintf("phenotype %d bytes exceeds %d", phenotypeSize, FissionPhenotypeBytes)}, nil
	}
	// Multiple genuinely distinct domains (a conservative >=3 bar avoids firing
	// on cells that merely carry a couple of descriptive tags).
	if desc != nil && len(distinctTags(desc.Semantics.DomainTags)) >= 3 {
		return TopologyPlan{Kind: ExecuteCellularFission, Target: target,
			Reason: fmt.Sprintf("cell spans %d domain tags", len(distinctTags(desc.Semantics.DomainTags)))}, nil
	}

	// Fusion: a tightly-coupled partner plus an elevated bridge latency.
	if gravity != nil && lastBridgeLatencyP99NS >= latencyThresholdNS {
		m, err := gravity.Load()
		if err != nil {
			return TopologyPlan{}, err
		}
		best, bestG := "", 0.0
		for _, p := range partners {
			if p == target {
				continue
			}
			if g := m.Gravity(target, p); g >= gravityThreshold && g > bestG {
				best, bestG = p, g
			}
		}
		if best != "" {
			return TopologyPlan{Kind: ExecuteCellularFusion, Target: target, Partner: best,
				Reason: fmt.Sprintf("gravity(%s,%s)=%.2f with bridge p99 %dns", target, best, bestG, lastBridgeLatencyP99NS)}, nil
		}
	}

	return TopologyPlan{Kind: MutateGenotypeOnly, Target: target, Reason: "no structural trigger"}, nil
}

func distinctTags(tags []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range tags {
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}

package evolution

import (
	"path/filepath"
	"testing"

	"github.com/mdcfrancis/flow/codependency"
	"github.com/mdcfrancis/flow/manifest"
	"github.com/mdcfrancis/flow/storage"
)

func TestPlanTopologyFissionBySize(t *testing.T) {
	desc := &manifest.NodeDescriptor{IdentityURN: "urn:a"}
	plan, err := PlanTopology("urn:a", desc, FissionPhenotypeBytes+1, nil, nil, 0,
		DefaultGravityThreshold, DefaultBridgeLatencyThresholdNS)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Kind != ExecuteCellularFission {
		t.Fatalf("kind = %v, want fission (oversized phenotype)", plan.Kind)
	}
}

func TestPlanTopologyFissionByDomains(t *testing.T) {
	desc := &manifest.NodeDescriptor{
		IdentityURN: "urn:a",
		Semantics:   manifest.SemanticManifest{DomainTags: []string{"auth", "billing", "reporting"}},
	}
	plan, _ := PlanTopology("urn:a", desc, 1024, nil, nil, 0,
		DefaultGravityThreshold, DefaultBridgeLatencyThresholdNS)
	if plan.Kind != ExecuteCellularFission {
		t.Fatalf("kind = %v, want fission (multiple domains)", plan.Kind)
	}
}

func TestPlanTopologyFusion(t *testing.T) {
	le, err := storage.NewLedgerEngine(filepath.Join(t.TempDir(), "hdm.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer le.Close()
	g := codependency.NewTracker(le)
	// Make urn:a and urn:b tightly coupled: 2 attempts, 2 joint failures => 1.0.
	g.RecordIsolationAttempt("urn:a")
	g.RecordIsolationAttempt("urn:a")
	g.RecordJointFailure("urn:a", "urn:b")
	g.RecordJointFailure("urn:a", "urn:b")

	desc := &manifest.NodeDescriptor{IdentityURN: "urn:a"}
	// Elevated bridge latency triggers the fusion evaluation.
	plan, err := PlanTopology("urn:a", desc, 1024, g, []string{"urn:b"}, 100_000,
		DefaultGravityThreshold, DefaultBridgeLatencyThresholdNS)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Kind != ExecuteCellularFusion || plan.Partner != "urn:b" {
		t.Fatalf("plan = %+v, want fusion with urn:b", plan)
	}

	// Without elevated latency, no fusion (bridge tax not worth removing).
	plan, _ = PlanTopology("urn:a", desc, 1024, g, []string{"urn:b"}, 0,
		DefaultGravityThreshold, DefaultBridgeLatencyThresholdNS)
	if plan.Kind != MutateGenotypeOnly {
		t.Fatalf("kind = %v, want genotype-only (low latency)", plan.Kind)
	}
}

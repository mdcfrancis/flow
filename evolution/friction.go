package evolution

import (
	"context"
	"fmt"
	"sort"

	"github.com/mdcfrancis/flow/manifest"
	"github.com/mdcfrancis/flow/telemetry"
)

// FrictionReport is one cell's measured energy, used for target selection.
type FrictionReport struct {
	URN         string
	Fuel        uint64
	Saliency    float64
	Hamiltonian float64
}

// ScanFriction probes each candidate cell,
// measuring its execution fuel in a shadow sandbox and computing its Hamiltonian
// energy from the fuel and the descriptor's saliency. Cells that fail to load or
// probe are skipped. Reports are returned sorted by descending energy — the
// highest-friction cell first.
func ScanFriction(ctx context.Context, repo *manifest.Repository, urns []string, entry string, probeArgs []uint64, tokenMilliCents uint32) ([]FrictionReport, error) {
	var reports []FrictionReport
	for _, urn := range urns {
		desc, err := repo.Load(urn)
		if err != nil {
			continue
		}
		phenotype, err := repo.Phenotype(desc)
		if err != nil {
			continue
		}
		res, err := execCell(ctx, phenotype, entry, probeArgs...)
		if err != nil {
			continue
		}
		h := telemetry.CalculateHamiltonian(telemetry.SystemMetrics{
			WasmFuel: res.Fuel, TokenMilliCents: tokenMilliCents, SaliencyScore: desc.Saliency,
		})
		reports = append(reports, FrictionReport{
			URN: urn, Fuel: res.Fuel, Saliency: desc.Saliency, Hamiltonian: h,
		})
	}
	sort.SliceStable(reports, func(i, j int) bool {
		return reports[i].Hamiltonian > reports[j].Hamiltonian
	})
	return reports, nil
}

// SelectTarget returns the highest-friction cell among the candidates, i.e. the
// one whose optimization would most reduce cluster energy.
func SelectTarget(ctx context.Context, repo *manifest.Repository, urns []string, entry string, probeArgs []uint64, tokenMilliCents uint32) (string, error) {
	reports, err := ScanFriction(ctx, repo, urns, entry, probeArgs, tokenMilliCents)
	if err != nil {
		return "", err
	}
	if len(reports) == 0 {
		return "", fmt.Errorf("no probeable cells among %d candidates", len(urns))
	}
	return reports[0].URN, nil
}

package telemetry

// SystemMetrics captures the runtime vectors of a single execution slice, used
// to compute the cluster's global Hamiltonian energy.
type SystemMetrics struct {
	LatencyNS       uint64  // cross-boundary execution latency, nanoseconds
	WasmFuel        uint64  // raw execution fuel (see FuelTracker)
	TokenMilliCents uint32  // cognitive parsing fee, in milli-cents
	SaliencyScore   float64 // semantic utility (reward term)
	CodeBytes       uint64  // cell code size — the REUSE/abstraction pressure
}

// Hamiltonian weighting constants, fixed at boot initialization. They map the
// physical runtime vectors onto a single scalar energy:
//
//	H = α·Latency + β·Fuel + γ·ComputeCost + ε·CodeBytes − δ·Saliency
const (
	Alpha   = 1.5e-6 // latency penalty: nanoseconds -> energy
	Beta    = 1.0e-4 // fuel expenditure scaling
	Gamma   = 2.5e-1 // cognitive token-cost index
	Delta   = 100.0  // semantic-utility reward weight
	Epsilon = 2.0e-6 // code-size penalty: a byte of cell code -> energy
)

// CalculateHamiltonian returns the global energy metric H for the given
// metrics. The evolutionary loop minimizes H: lower is better. Mutations that
// reduce friction (latency, fuel, token cost, code size) or raise saliency lower H.
//
// The CodeBytes term is the REUSE / abstraction pressure: fuel alone rewards only
// call/dispatch reduction, so the loop inlines everything and never prefers a shared
// primitive (dispatch adds crossings). Charging for a cell's own code size makes OFFLOADING
// inline logic to a dispatched shared primitive a net win — the cell shrinks a lot while fuel
// rises a little — so the system builds and reuses abstractions (queues, heaps, …) whose
// value is algorithmic or DRY, not raw crossings. The weight is small, so a genuine fuel win
// (e.g. a cache that eliminates dispatches) still dominates.
func CalculateHamiltonian(m SystemMetrics) float64 {
	return Alpha*float64(m.LatencyNS) +
		Beta*float64(m.WasmFuel) +
		Gamma*float64(m.TokenMilliCents) +
		Epsilon*float64(m.CodeBytes) -
		Delta*m.SaliencyScore
}

package main

import "sync"

// frameBudget tracks each sync cell's per-frame execution cost — the natural cost a
// 30 FPS app must respect. Each frame has ~frameNS to spend; a cell that overruns
// its share makes animation stutter, so an over-budget cell is re-opened for
// efficiency optimization (the Hamiltonian already rewards lower latency/fuel) until
// it fits. Written by the frame loop, read by the fixpoint pass and the /perf
// endpoint, so it carries its own lock.
type frameBudget struct {
	mu      sync.Mutex
	emaNS   map[string]float64 // per-cell exponential moving average of guest ns/frame
	frameNS float64            // wall-clock budget of one whole frame (1e9 / fps)
}

func newFrameBudget(fps int) *frameBudget {
	if fps <= 0 {
		fps = 30
	}
	return &frameBudget{emaNS: map[string]float64{}, frameNS: 1e9 / float64(fps)}
}

// budgetHeadroom leaves room in each frame for the render poll, host compositing,
// and scheduler overhead beyond the sync cells' own compute.
const budgetHeadroom = 0.8

// record folds a cell's measured per-frame cost into its EMA (α=0.2 — smooths jitter
// while still tracking a genuine regression within a few frames).
func (b *frameBudget) record(urn string, ns float64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if prev, ok := b.emaNS[urn]; ok {
		b.emaNS[urn] = 0.8*prev + 0.2*ns
	} else {
		b.emaNS[urn] = ns
	}
}

// perCellBudgetNS is the ns each of syncCount sync cells may spend per frame.
func (b *frameBudget) perCellBudgetNS(syncCount int) float64 {
	if syncCount < 1 {
		syncCount = 1
	}
	return b.frameNS * budgetHeadroom / float64(syncCount)
}

// overBudget reports whether a cell's EMA cost exceeds its share of the frame.
func (b *frameBudget) overBudget(urn string, syncCount int) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	ema, ok := b.emaNS[urn]
	return ok && ema > b.perCellBudgetNS(syncCount)
}

// avgNS returns a cell's EMA cost (0 if never recorded).
func (b *frameBudget) avgNS(urn string) float64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.emaNS[urn]
}

// tracked returns the set of cells with a recorded cost (snapshot).
func (b *frameBudget) tracked() map[string]float64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make(map[string]float64, len(b.emaNS))
	for k, v := range b.emaNS {
		out[k] = v
	}
	return out
}

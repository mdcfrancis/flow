package evolution

import (
	"encoding/json"
	"fmt"

	"github.com/mdcfrancis/flow/storage"
)

// The SYSTEM's own acceptance. Cutting a new epoch (a new baseline the next run starts from)
// must be EARNED: the system evolves a fixed suite of benchmark applications and must prove it
// is at least as CORRECT and strictly more EFFICIENT at evolving them than the last epoch. This
// is the system-level analogue of a cell's acceptance scenarios and the visual meta-acceptance
// corpus — a regression + performance gate on the whole machine.

// BenchmarkAppResult is how the current system did evolving one benchmark app.
type BenchmarkAppResult struct {
	Name      string `json:"name"`
	Converged bool   `json:"converged"`
	Green     int    `json:"green"`
	Total     int    `json:"total"`
	Tokens    uint64 `json:"tokens"` // tokens spent evolving this app
	Frames    int    `json:"frames"` // mutation frames spent
}

// BenchmarkResult is a full run of the benchmark suite — the system's report card.
type BenchmarkResult struct {
	Epoch       string               `json:"epoch"`
	Apps        []BenchmarkAppResult `json:"apps"`
	Correctness float64              `json:"correctness"` // fraction of benchmark apps that converged
	Tokens      uint64               `json:"tokens"`      // total tokens across the suite
	Frames      int                  `json:"frames"`      // total mutation frames across the suite
	At          int64                `json:"at"`          // unix seconds (caller-stamped)
}

// summarize fills Correctness/Tokens/Frames from the per-app results.
func (r *BenchmarkResult) Summarize() {
	if len(r.Apps) == 0 {
		return
	}
	conv := 0
	r.Tokens, r.Frames = 0, 0
	for _, a := range r.Apps {
		if a.Converged {
			conv++
		}
		r.Tokens += a.Tokens
		r.Frames += a.Frames
	}
	r.Correctness = float64(conv) / float64(len(r.Apps))
}

const benchmarkBaselineRef = "urn:hdm:system:benchmark-baseline"

// LoadBenchmarkBaseline returns the last promoted epoch's benchmark, or nil if none.
func LoadBenchmarkBaseline(ledger *storage.LedgerEngine) *BenchmarkResult {
	h, err := ledger.GetRef(benchmarkBaselineRef)
	if err != nil {
		return nil
	}
	raw, err := ledger.ReadBlock(h)
	if err != nil {
		return nil
	}
	var r BenchmarkResult
	if json.Unmarshal(raw, &r) != nil {
		return nil
	}
	return &r
}

// SaveBenchmarkBaseline records the benchmark that a newly-promoted epoch was cut against — the
// bar the NEXT epoch must beat.
func SaveBenchmarkBaseline(ledger *storage.LedgerEngine, r BenchmarkResult) error {
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	h, err := ledger.WriteBlock(raw)
	if err != nil {
		return err
	}
	return ledger.UpdateRef(benchmarkBaselineRef, h)
}

// ResetBenchmarkBaseline clears the recorded baseline so the next promotion establishes a fresh
// one. Use it when the benchmark's MEASURE changes (e.g. tokens now count the grow, not just the
// evolution) — an old baseline under the previous measure is not comparable to a new run.
func ResetBenchmarkBaseline(ledger *storage.LedgerEngine) error {
	return ledger.DeleteRefs(benchmarkBaselineRef)
}

// ShouldPromote is the epoch-promotion gate: a new epoch is cut ONLY if the system did not
// REGRESS in correctness AND it improved — either more correct, or equally correct but more
// efficient at evolving (fewer tokens, or same tokens with fewer frames). The first run (no
// baseline) always promotes to establish the bar. Correctness is never traded for efficiency.
func ShouldPromote(next BenchmarkResult, baseline *BenchmarkResult) (bool, string) {
	if baseline == nil {
		return true, "first benchmark — establishing the baseline"
	}
	const eps = 1e-9
	if next.Correctness < baseline.Correctness-eps {
		return false, fmt.Sprintf("REGRESSED: correctness %.0f%% < baseline %.0f%%", next.Correctness*100, baseline.Correctness*100)
	}
	if next.Correctness > baseline.Correctness+eps {
		return true, fmt.Sprintf("more correct: %.0f%% vs %.0f%%", next.Correctness*100, baseline.Correctness*100)
	}
	// Equal correctness: promote only if strictly more efficient at evolving.
	if next.Tokens < baseline.Tokens {
		return true, fmt.Sprintf("equal correctness (%.0f%%), more efficient: %d vs %d tokens", next.Correctness*100, next.Tokens, baseline.Tokens)
	}
	if next.Tokens == baseline.Tokens && next.Frames < baseline.Frames {
		return true, fmt.Sprintf("equal correctness + tokens, fewer frames: %d vs %d", next.Frames, baseline.Frames)
	}
	return false, fmt.Sprintf("no gain: correctness %.0f%%, tokens %d vs %d, frames %d vs %d — not promoting",
		next.Correctness*100, next.Tokens, baseline.Tokens, next.Frames, baseline.Frames)
}

package evolution

import (
	"path/filepath"
	"testing"

	"github.com/mdcfrancis/flow/storage"
)

func TestShouldPromote(t *testing.T) {
	base := &BenchmarkResult{Correctness: 1.0, Tokens: 1000, Frames: 20}
	cases := []struct {
		name string
		next BenchmarkResult
		want bool
	}{
		{"first-run-no-baseline", BenchmarkResult{Correctness: 0.5, Tokens: 999}, true}, // baseline=nil below
		{"correctness-regressed", BenchmarkResult{Correctness: 0.8, Tokens: 500}, false},
		{"equal-no-gain", BenchmarkResult{Correctness: 1.0, Tokens: 1000, Frames: 20}, false}, // identical to baseline
		{"equal-correct-more-efficient", BenchmarkResult{Correctness: 1.0, Tokens: 800, Frames: 20}, true},
		{"equal-correct-same-tokens-fewer-frames", BenchmarkResult{Correctness: 1.0, Tokens: 1000, Frames: 15}, true},
		{"no-gain", BenchmarkResult{Correctness: 1.0, Tokens: 1200, Frames: 25}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := base
			if c.name == "first-run-no-baseline" {
				b = nil
			}
			got, reason := ShouldPromote(c.next, b)
			if got != c.want {
				t.Errorf("ShouldPromote=%v want %v (%s)", got, c.want, reason)
			}
		})
	}
}

func TestBenchmarkBaselineRoundTrip(t *testing.T) {
	le, _ := storage.NewLedgerEngine(filepath.Join(t.TempDir(), "hdm.db"))
	defer le.Close()
	if LoadBenchmarkBaseline(le) != nil {
		t.Fatal("expected no baseline")
	}
	r := BenchmarkResult{Epoch: "v1", Apps: []BenchmarkAppResult{{Name: "counter", Converged: true, Tokens: 500, Frames: 10}}}
	r.Summarize()
	if r.Correctness != 1.0 || r.Tokens != 500 {
		t.Fatalf("summarize wrong: %+v", r)
	}
	_ = SaveBenchmarkBaseline(le, r)
	got := LoadBenchmarkBaseline(le)
	if got == nil || got.Epoch != "v1" || got.Tokens != 500 {
		t.Fatalf("round-trip: %+v", got)
	}
}

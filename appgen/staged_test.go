package appgen

import (
	"context"
	"testing"
)

// constModel returns the same output for every call — enough for scaffolding
// (genesis skeleton compiles; WIT/acceptance fall back), while a fakeRunner
// stands in for the evolutionary driver.
type constModel struct{ out string }

func (m constModel) InvokeReasoning(ctx context.Context, sys, user string) (string, error) {
	return m.out, nil
}

// fakeRunner simulates a cell that passes its acceptance suite after a fixed
// number of mutation frames, and records the order frames were run in so a test
// can assert strict stage gating.
type fakeRunner struct {
	require map[string]int
	frames  map[string]int
	order   []string
}

func newFakeRunner(require map[string]int) *fakeRunner {
	return &fakeRunner{require: require, frames: map[string]int{}}
}

func (f *fakeRunner) ScoreCell(ctx context.Context, urn string) (int, int, error) {
	req := f.require[urn]
	p := f.frames[urn]
	if p > req {
		p = req
	}
	return p, req, nil
}

func (f *fakeRunner) RunFrame(ctx context.Context, urn string) error {
	f.frames[urn]++
	f.order = append(f.order, urn)
	return nil
}

func TestRunStagesGatesInOrder(t *testing.T) {
	g, repo := newGrower(t, constModel{out: goodSkeleton})
	env := &AppEnvelope{
		ApplicationNamespace: "urn:hdm:apps:demo",
		SubsystemRequirements: []Subsystem{
			{Identity: "urn:hdm:apps:demo:a", Semantics: "track a sliding window"},
			{Identity: "urn:hdm:apps:demo:b", Semantics: "emit a summary"},
		},
	}
	fake := newFakeRunner(map[string]int{
		"urn:hdm:apps:demo:a": 2, // needs two frames to pass
		"urn:hdm:apps:demo:b": 1,
	})
	var enrolled []string
	res, err := g.RunStages(context.Background(), env, fake, 5, func(u string) { enrolled = append(enrolled, u) })
	if err != nil {
		t.Fatalf("run stages: %v", err)
	}

	if len(res.Stages) != 2 ||
		res.Stages[0].URN != "urn:hdm:apps:demo:a" || res.Stages[1].URN != "urn:hdm:apps:demo:b" {
		t.Fatalf("stage order wrong: %+v", res.Stages)
	}
	if !res.Stages[0].Done || res.Stages[0].Attempts != 2 {
		t.Fatalf("stage A: %+v (want Done, 2 attempts)", res.Stages[0])
	}
	if !res.Stages[1].Done || res.Stages[1].Attempts != 1 {
		t.Fatalf("stage B: %+v (want Done, 1 attempt)", res.Stages[1])
	}
	if len(enrolled) != 2 || enrolled[0] != "urn:hdm:apps:demo:a" || enrolled[1] != "urn:hdm:apps:demo:b" {
		t.Fatalf("enrolled order wrong: %v", enrolled)
	}
	// Strict gating: stage A is driven to completion (a, a) before stage B (b)
	// is ever touched.
	want := []string{"urn:hdm:apps:demo:a", "urn:hdm:apps:demo:a", "urn:hdm:apps:demo:b"}
	if len(fake.order) != len(want) {
		t.Fatalf("frame order = %v, want %v", fake.order, want)
	}
	for i := range want {
		if fake.order[i] != want[i] {
			t.Fatalf("frame order = %v, want %v", fake.order, want)
		}
	}
	// Each stage left a live, resolvable cell.
	for _, u := range enrolled {
		if _, err := repo.Load(u); err != nil {
			t.Fatalf("%s not live: %v", u, err)
		}
	}
}

func TestRunStagesBudgetCapAdvances(t *testing.T) {
	g, _ := newGrower(t, constModel{out: goodSkeleton})
	env := &AppEnvelope{
		ApplicationNamespace:  "urn:hdm:apps:hard",
		SubsystemRequirements: []Subsystem{{Identity: "urn:hdm:apps:hard:core", Semantics: "do a hard thing"}},
	}
	fake := newFakeRunner(map[string]int{"urn:hdm:apps:hard:core": 10}) // never satisfiable within budget
	res, err := g.RunStages(context.Background(), env, fake, 3, nil)
	if err != nil {
		t.Fatalf("run stages: %v", err)
	}
	s := res.Stages[0]
	if s.Done {
		t.Fatalf("stage should be unfinished under a 3-frame budget: %+v", s)
	}
	if s.Attempts != 3 {
		t.Fatalf("attempts = %d, want 3 (budget cap)", s.Attempts)
	}
}

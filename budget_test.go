package main

import "testing"

func TestFrameBudgetEMAAndOverBudget(t *testing.T) {
	b := newFrameBudget(30) // frameNS = 33.3ms
	// one sync cell -> budget = 33.3ms * 0.8 = ~26.6ms
	cheap := "urn:hdm:apps:x:motion"
	for i := 0; i < 5; i++ {
		b.record(cheap, 100_000) // 0.1ms/frame
	}
	if b.overBudget(cheap, 1) {
		t.Errorf("0.1ms cell must be well under a ~26ms budget; avg=%.0fns", b.avgNS(cheap))
	}

	// an expensive cell over its share of the frame
	heavy := "urn:hdm:apps:x:heavy"
	for i := 0; i < 10; i++ {
		b.record(heavy, 30_000_000) // 30ms/frame
	}
	if !b.overBudget(heavy, 1) {
		t.Errorf("30ms cell must be over a ~26ms budget; avg=%.0fns", b.avgNS(heavy))
	}
	// with more sync cells the per-cell budget shrinks — still over
	if !b.overBudget(heavy, 4) {
		t.Error("30ms cell must be over budget when the frame is split 4 ways")
	}

	// EMA tracks toward new cost rather than snapping
	one := "urn:hdm:apps:x:one"
	b.record(one, 1_000_000)
	b.record(one, 5_000_000)
	if a := b.avgNS(one); a <= 1_000_000 || a >= 5_000_000 {
		t.Errorf("EMA should be between the two samples, got %.0f", a)
	}
}

package main

import (
	"fmt"
	"sync"
	"testing"
)

func TestLogRingResetClearsAndReportsCount(t *testing.T) {
	r := newLogRing(10)
	for i := 0; i < 4; i++ {
		if _, err := fmt.Fprintf(r, "line %d\n", i); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	if got := len(r.Lines()); got != 4 {
		t.Fatalf("buffered lines = %d, want 4", got)
	}
	if n := r.Reset(); n != 4 {
		t.Fatalf("Reset reported %d cleared, want 4", n)
	}
	if got := len(r.Lines()); got != 0 {
		t.Fatalf("after reset lines = %d, want 0", got)
	}
	// The ring must stay usable after a reset — the console keeps writing into it.
	fmt.Fprintln(r, "after")
	if got := r.Lines(); len(got) != 1 || got[0] != "after" {
		t.Fatalf("post-reset lines = %v, want [after]", got)
	}
	if n := r.Reset(); n != 1 {
		t.Fatalf("second Reset reported %d, want 1", n)
	}
}

// Reset takes the same lock as Write/Lines; run them together so the race
// detector would catch a reset that reads or truncates lines unguarded.
func TestLogRingResetConcurrentWithWrites(t *testing.T) {
	r := newLogRing(50)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				fmt.Fprintf(r, "w%d-%d\n", n, j)
			}
		}(i)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for j := 0; j < 50; j++ {
			r.Reset()
			_ = r.Lines()
		}
	}()
	wg.Wait()
	// Only the absence of a race/panic is asserted here; the surviving line count
	// is timing-dependent by construction.
	if got := len(r.Lines()); got > 50 {
		t.Fatalf("ring exceeded capacity: %d", got)
	}
}

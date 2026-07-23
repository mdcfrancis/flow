package status

import "testing"

func TestBrokerPhaseAndEvents(t *testing.T) {
	b := New()
	b.Phase("synthesizing", "reasoning a candidate for urn:x", "urn:x")
	b.Ticks(10, 16)

	// A create event registers the cell as new.
	b.Event("create", "urn:x", "genesis")
	// A commit transitions it to live.
	b.Event("commit", "urn:x", "H -12%")
	// A fission splits a second cell.
	b.Event("fission", "urn:y", "core+dispatch")

	s := b.Snapshot()
	if s.Phase != "synthesizing" || s.Target != "urn:x" {
		t.Fatalf("phase/target = %q/%q", s.Phase, s.Target)
	}
	if s.NextFrameTick != 16 || s.Tick != 10 {
		t.Fatalf("ticks = %d/%d", s.Tick, s.NextFrameTick)
	}
	if len(s.Events) != 3 {
		t.Fatalf("events = %d, want 3", len(s.Events))
	}
	if s.Events[0].Seq != 1 || s.Events[2].Seq != 3 {
		t.Fatalf("event seq ordering wrong: %+v", s.Events)
	}
	stateOf := map[string]string{}
	for _, c := range s.Cells {
		stateOf[c.URN] = c.State
	}
	if stateOf["urn:x"] != CellLive {
		t.Fatalf("urn:x state = %q, want live (commit follows create)", stateOf["urn:x"])
	}
	if stateOf["urn:y"] != CellSplit {
		t.Fatalf("urn:y state = %q, want split", stateOf["urn:y"])
	}
}

func TestBrokerRingBufferBounds(t *testing.T) {
	b := New()
	for i := 0; i < maxEvents+25; i++ {
		b.Event("mutate", "urn:x", "iter")
	}
	s := b.Snapshot()
	if len(s.Events) != maxEvents {
		t.Fatalf("events retained = %d, want %d", len(s.Events), maxEvents)
	}
	// Oldest retained event must have the correct sequence (ring dropped the front).
	if s.Events[0].Seq != uint64(25+1) {
		t.Fatalf("front seq = %d, want %d", s.Events[0].Seq, 26)
	}
}

func TestBrokerNilSafe(t *testing.T) {
	var b *Broker
	b.Phase("x", "y", "z") // must not panic
	b.Event("create", "urn:x", "")
	b.Flow("infer", "src", "sum") // must not panic
	if got := b.Snapshot(); got.Phase != "" {
		t.Fatalf("nil snapshot = %+v", got)
	}
}

func TestBrokerFlowLog(t *testing.T) {
	b := New()
	b.Flow("infer", "GENESIS SYNTHESIS", "1.2KB→340B · 512 tok · 8100ms")
	b.Flow("tick", "81f800", "tick 1 → 1 · fuel 2")
	s := b.Snapshot()
	if len(s.Flow) != 2 || s.Flow[0].Kind != "infer" || s.Flow[1].Kind != "tick" {
		t.Fatalf("flow = %+v", s.Flow)
	}
	if s.Flow[0].Seq != 1 || s.Flow[1].Seq != 2 {
		t.Fatalf("flow seq ordering wrong: %+v", s.Flow)
	}
	// Ring buffer bound.
	for i := 0; i < maxFlow+10; i++ {
		b.Flow("tick", "x", "y")
	}
	if got := len(b.Snapshot().Flow); got != maxFlow {
		t.Fatalf("flow retained = %d, want %d", got, maxFlow)
	}
}

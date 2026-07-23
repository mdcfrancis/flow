// Package status is a small, thread-safe activity broker: the runtime pushes
// phase transitions and cell-lifecycle events into it, and the edge console
// polls a JSON snapshot to show what the system is doing and — when it is idle —
// why it is waiting. It is a leaf package (standard library only) so any layer
// can import it without creating a cycle.
package status

import (
	"sync"
	"time"
)

// maxEvents bounds the retained lifecycle-event ring buffer; maxFlow bounds the
// finer-grained data-flow log.
const (
	maxEvents = 60
	maxFlow   = 120
)

// Cell lifecycle states surfaced to the UI.
const (
	CellLive     = "live"     // steady state
	CellMutating = "mutating" // a mutation frame is refining this cell
	CellNew      = "new"      // freshly created (growth)
	CellSplit    = "split"    // result of a fission
	CellFused    = "fused"    // result of a fusion
	CellRejected = "rejected" // a candidate was graded and held
)

// Event is one entry in the activity feed.
type Event struct {
	Seq    uint64 `json:"seq"`
	TS     int64  `json:"ts"` // unix milliseconds
	Kind   string `json:"kind"`
	Cell   string `json:"cell,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// FlowEvent is one entry in the data-flow log: a concrete piece of data moving
// through the system (an inference round-trip, a tick result, an input event).
// Unlike Event (cell lifecycle), FlowEvent captures what is actually flowing.
type FlowEvent struct {
	Seq     uint64 `json:"seq"`
	TS      int64  `json:"ts"`     // unix milliseconds
	Kind    string `json:"kind"`   // infer | tick | input | ingress | render
	Source  string `json:"source"` // where it came from (e.g. the reasoning purpose)
	Summary string `json:"summary"`
}

// CellState is the current lifecycle state of a known cell.
type CellState struct {
	URN     string `json:"urn"`
	State   string `json:"state"`
	Updated int64  `json:"updated"`
}

// Snapshot is the immutable JSON view the console polls.
type Snapshot struct {
	Phase         string      `json:"phase"`
	Reason        string      `json:"reason"`
	Target        string      `json:"target,omitempty"`
	Tick          int         `json:"tick"`
	NextFrameTick int         `json:"nextFrameTick"`
	Cells         []CellState `json:"cells"`
	Events        []Event     `json:"events"`
	Flow          []FlowEvent `json:"flow"`
}

// Broker holds the live activity state. The zero value is not usable; call New.
type Broker struct {
	mu            sync.Mutex
	seq           uint64
	phase         string
	reason        string
	target        string
	tick          int
	nextFrameTick int
	cells         map[string]*CellState
	order         []string // stable display order (insertion order)
	events        []Event
	flow          []FlowEvent
	flowSeq       uint64
	now           func() int64
}

// Flow records a concrete piece of data moving through the system for the live
// flow log. Nil-safe and thread-safe.
func (b *Broker) Flow(kind, source, summary string) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.flowSeq++
	b.flow = append(b.flow, FlowEvent{Seq: b.flowSeq, TS: b.now(), Kind: kind, Source: source, Summary: summary})
	if len(b.flow) > maxFlow {
		b.flow = b.flow[len(b.flow)-maxFlow:]
	}
}

// New returns an idle broker.
func New() *Broker {
	return &Broker{
		phase: "booting",
		cells: make(map[string]*CellState),
		now:   func() int64 { return time.Now().UnixMilli() },
	}
}

// Phase records what the system is doing now (and, when idle/blocked, why it is
// waiting). target is the cell in focus, if any.
func (b *Broker) Phase(phase, reason, target string) {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.phase, b.reason, b.target = phase, reason, target
	b.mu.Unlock()
}

// Reason updates only the waiting/explanation text, leaving the phase intact.
func (b *Broker) Reason(reason string) {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.reason = reason
	b.mu.Unlock()
}

// Ticks records the heartbeat counter and the tick at which the next
// evolutionary frame is scheduled, so the UI can show a countdown.
func (b *Broker) Ticks(tick, nextFrameTick int) {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.tick, b.nextFrameTick = tick, nextFrameTick
	b.mu.Unlock()
}

// SetCell records a cell's lifecycle state, registering it on first sight.
func (b *Broker) SetCell(urn, state string) {
	if b == nil || urn == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.setCellLocked(urn, state)
}

func (b *Broker) setCellLocked(urn, state string) {
	cs, ok := b.cells[urn]
	if !ok {
		cs = &CellState{URN: urn}
		b.cells[urn] = cs
		b.order = append(b.order, urn)
	}
	cs.State = state
	cs.Updated = b.now()
}

// Event appends an activity event and, for lifecycle kinds, transitions the
// referenced cell's state so the visual stays in sync with the feed.
func (b *Broker) Event(kind, cell, detail string) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.seq++
	b.events = append(b.events, Event{Seq: b.seq, TS: b.now(), Kind: kind, Cell: cell, Detail: detail})
	if len(b.events) > maxEvents {
		b.events = b.events[len(b.events)-maxEvents:]
	}
	if cell != "" {
		if s := cellStateForKind(kind); s != "" {
			b.setCellLocked(cell, s)
		}
	}
}

// cellStateForKind maps an event kind onto the lifecycle state it implies.
func cellStateForKind(kind string) string {
	switch kind {
	case "create", "grow":
		return CellNew
	case "mutate":
		return CellMutating
	case "fission":
		return CellSplit
	case "fusion":
		return CellFused
	case "commit":
		return CellLive
	case "reject", "hold":
		return CellRejected
	default:
		return ""
	}
}

// Snapshot returns a deep copy safe to serialize without holding the lock.
func (b *Broker) Snapshot() Snapshot {
	if b == nil {
		return Snapshot{}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	cells := make([]CellState, 0, len(b.order))
	for _, urn := range b.order {
		cells = append(cells, *b.cells[urn])
	}
	events := make([]Event, len(b.events))
	copy(events, b.events)
	flow := make([]FlowEvent, len(b.flow))
	copy(flow, b.flow)
	return Snapshot{
		Phase: b.phase, Reason: b.reason, Target: b.target,
		Tick: b.tick, NextFrameTick: b.nextFrameTick,
		Cells: cells, Events: events, Flow: flow,
	}
}

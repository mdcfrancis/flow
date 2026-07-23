package telemetry

// Package telemetry implements the HDM Hamiltonian physics matrix: a non-
// intrusive fuel accounting apparatus that hooks wazero's function-call
// lifecycle to measure execution cost without injecting observation overhead
// into guest code, plus the global energy valuation used to grade mutations.

import (
	"context"
	"sort"
	"sync"
	"sync/atomic"

	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/experimental"
)

// FuelTracker accumulates the raw execution fuel counted across the wazero
// function-call lifecycle, and records which functions were entered (a
// coverage signature). It is safe for concurrent use.
type FuelTracker struct {
	instructionCount atomic.Uint64
	mu               sync.Mutex
	funcs            map[uint32]bool
}

// Add increments the fuel tally.
func (f *FuelTracker) Add(n uint64) { f.instructionCount.Add(n) }

// Refund decrements the fuel tally (saturating at 0). Used to make dispatch to a SHARED,
// verified sys:* primitive essentially free: the primitive is amortized infrastructure, so its
// execution cost is refunded from the caller rather than charged at every call site.
func (f *FuelTracker) Refund(n uint64) {
	for {
		cur := f.instructionCount.Load()
		next := uint64(0)
		if cur > n {
			next = cur - n
		}
		if f.instructionCount.CompareAndSwap(cur, next) {
			return
		}
	}
}

// Fuel returns the accumulated fuel count.
func (f *FuelTracker) Fuel() uint64 { return f.instructionCount.Load() }

// mark records that the function with the given index was entered.
func (f *FuelTracker) mark(idx uint32) {
	f.mu.Lock()
	if f.funcs == nil {
		f.funcs = make(map[uint32]bool)
	}
	f.funcs[idx] = true
	f.mu.Unlock()
}

// Coverage returns the sorted set of function indices entered so far — the
// execution's code-coverage signature.
func (f *FuelTracker) Coverage() []uint32 {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]uint32, 0, len(f.funcs))
	for k := range f.funcs {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Reset zeroes the fuel tally and coverage, e.g. between shadow replay passes.
func (f *FuelTracker) Reset() {
	f.instructionCount.Store(0)
	f.mu.Lock()
	f.funcs = nil
	f.mu.Unlock()
}

// telemetryListener counts one fuel unit per function-boundary crossing and
// records the entered function. It implements experimental.FunctionListener.
type telemetryListener struct {
	tracker *FuelTracker
}

// Before is invoked at each function entry: this is where a crossed basic-block
// boundary is tallied, matching the atomic execution-step accounting model.
func (l *telemetryListener) Before(_ context.Context, _ api.Module, def api.FunctionDefinition, _ []uint64, _ experimental.StackIterator) {
	l.tracker.Add(1)
	l.tracker.mark(def.Index())
}

// After satisfies FunctionListener; fuel is booked on entry, so this is a nop.
func (l *telemetryListener) After(context.Context, api.Module, api.FunctionDefinition, []uint64) {
}

// Abort satisfies FunctionListener. A trapped/panicked call still consumed the
// fuel booked in Before, so no adjustment is made.
func (l *telemetryListener) Abort(context.Context, api.Module, api.FunctionDefinition, error) {
}

// ListenerFactory produces telemetry listeners bound to a shared FuelTracker.
// A single listener instance is reused across all functions to avoid allocating
// per-function state.
type ListenerFactory struct {
	tracker  *FuelTracker
	listener *telemetryListener
}

// NewListenerFactory builds a factory feeding the given tracker.
func NewListenerFactory(tracker *FuelTracker) *ListenerFactory {
	return &ListenerFactory{
		tracker:  tracker,
		listener: &telemetryListener{tracker: tracker},
	}
}

// NewFunctionListener satisfies experimental.FunctionListenerFactory.
func (f *ListenerFactory) NewFunctionListener(api.FunctionDefinition) experimental.FunctionListener {
	return f.listener
}

// Instrument returns a context that installs fuel accounting for every module
// instantiated with it. Pass the returned context to the wazero runtime and to
// InstantiateModule so the listener factory propagates to compiled cells.
func Instrument(ctx context.Context, tracker *FuelTracker) context.Context {
	return experimental.WithFunctionListenerFactory(ctx, NewListenerFactory(tracker))
}

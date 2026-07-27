package execution

import "github.com/mdcfrancis/flow/stdlib"

// Functional combinators, P4: LAZY STREAMS.
// True laziness in the cell substrate is a pull-based generator protocol: a STREAM
// is a (state block, step function CELL) pair. step(statePtr) advances the private
// state, writes the next value to the value slot (state[0]), and returns 1 if it
// yielded a value or 0 if exhausted. Consumers PULL — computing values on demand and
// stopping early (take_while) — and transforms (map) wrap a source stream without
// materializing it. Best at coarse granularity (dispatch per pull); the inner hot
// loop stays an imperative leaf.

const (
	SysStreamFoldURN      = "urn:hdm:sys:stream-fold"
	SysStreamTakeWhileURN = "urn:hdm:sys:stream-take-while"
	SysStreamMapURN       = "urn:hdm:sys:stream-map"
)

// StreamCombinators returns the lazy-stream library (seeded alongside the eager
// combinators).
func StreamCombinators() []Combinator {
	return []Combinator{
		{SysStreamFoldURN, SysStreamFoldWAT, "stream_fold: pull a stream, acc=fn(acc,value) until exhausted; config {stepPtr,stepLen,statePtr,stateBytes,fnPtr,fnLen,seed,argBufPtr,max}"},
		{SysStreamTakeWhileURN, SysStreamTakeWhileWAT, "stream_take_while: pull while pred(value)!=0, LAZY early stop; config {stepPtr,stepLen,statePtr,stateBytes,predPtr,predLen,max}; returns count"},
		{SysStreamMapURN, SysStreamMapWAT, "stream_map: a step cell that wraps a source stream + fn (lazy transform); state {value,srcStatePtr,srcStepPtr,srcStepLen,fnPtr,fnLen,srcStateBytes}"},
	}
}

// SysStreamFoldWAT pulls a stream and folds it: acc = fn(acc, value) until step
// returns 0 (or max pulls, a safety bound). fn reads [acc, value] from argBuf.
var SysStreamFoldWAT = stdlib.MustCell("stream-fold")

// SysStreamTakeWhileWAT pulls while pred(value)!=0 and returns how many leading
// values satisfied it — LAZY: it stops pulling (computing) the moment pred fails.
var SysStreamTakeWhileWAT = stdlib.MustCell("stream-take-while")

// SysStreamMapWAT is a stream STEP cell that lazily maps a source stream: pull the
// source; if it yielded, apply fn to the source value and yield the result. Its
// state is {value, srcStatePtr, srcStepPtr, srcStepLen, fnPtr, fnLen, srcStateBytes}.
// Because it is itself a step cell, take_while/fold pulling it pulls the source —
// a genuinely lazy pipeline that materializes nothing.
var SysStreamMapWAT = stdlib.MustCell("stream-map")

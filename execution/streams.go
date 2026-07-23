package execution

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
const SysStreamFoldWAT = `(module
  (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 100))
  (import "hdm:kernel/cell-dispatch" "invoke-cell" (func $invoke (param i32 i32 i32 i32) (result i32)))
  (func (export "run-tick") (param $cfg i32) (param $len i32) (result i32)
    (local $stepP i32) (local $stepL i32) (local $st i32) (local $sb i32) (local $fnP i32) (local $fnL i32)
    (local $acc i32) (local $arg i32) (local $max i32) (local $i i32)
    (local.set $stepP (i32.load offset=0  (local.get $cfg)))
    (local.set $stepL (i32.load offset=4  (local.get $cfg)))
    (local.set $st    (i32.load offset=8  (local.get $cfg)))
    (local.set $sb    (i32.load offset=12 (local.get $cfg)))
    (local.set $fnP   (i32.load offset=16 (local.get $cfg)))
    (local.set $fnL   (i32.load offset=20 (local.get $cfg)))
    (local.set $acc   (i32.load offset=24 (local.get $cfg)))
    (local.set $arg   (i32.load offset=28 (local.get $cfg)))
    (local.set $max   (i32.load offset=32 (local.get $cfg)))
    (local.set $i (i32.const 0))
    (block $done (loop $loop
      (br_if $done (i32.ge_u (local.get $i) (local.get $max)))
      (br_if $done (i32.eqz (call $invoke (local.get $stepP) (local.get $stepL) (local.get $st) (local.get $sb))))
      (i32.store (local.get $arg) (local.get $acc))
      (i32.store (i32.add (local.get $arg) (i32.const 4)) (i32.load (local.get $st)))
      (local.set $acc (call $invoke (local.get $fnP) (local.get $fnL) (local.get $arg) (i32.const 8)))
      (local.set $i (i32.add (local.get $i) (i32.const 1)))
      (br $loop)))
    (local.get $acc)))`

// SysStreamTakeWhileWAT pulls while pred(value)!=0 and returns how many leading
// values satisfied it — LAZY: it stops pulling (computing) the moment pred fails.
const SysStreamTakeWhileWAT = `(module
  (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 100))
  (import "hdm:kernel/cell-dispatch" "invoke-cell" (func $invoke (param i32 i32 i32 i32) (result i32)))
  (func (export "run-tick") (param $cfg i32) (param $len i32) (result i32)
    (local $stepP i32) (local $stepL i32) (local $st i32) (local $sb i32) (local $prP i32) (local $prL i32)
    (local $max i32) (local $count i32)
    (local.set $stepP (i32.load offset=0  (local.get $cfg)))
    (local.set $stepL (i32.load offset=4  (local.get $cfg)))
    (local.set $st    (i32.load offset=8  (local.get $cfg)))
    (local.set $sb    (i32.load offset=12 (local.get $cfg)))
    (local.set $prP   (i32.load offset=16 (local.get $cfg)))
    (local.set $prL   (i32.load offset=20 (local.get $cfg)))
    (local.set $max   (i32.load offset=24 (local.get $cfg)))
    (local.set $count (i32.const 0))
    (block $done (loop $loop
      (br_if $done (i32.ge_u (local.get $count) (local.get $max)))
      (br_if $done (i32.eqz (call $invoke (local.get $stepP) (local.get $stepL) (local.get $st) (local.get $sb))))
      (br_if $done (i32.eqz (call $invoke (local.get $prP) (local.get $prL) (local.get $st) (i32.const 4))))
      (local.set $count (i32.add (local.get $count) (i32.const 1)))
      (br $loop)))
    (local.get $count)))`

// SysStreamMapWAT is a stream STEP cell that lazily maps a source stream: pull the
// source; if it yielded, apply fn to the source value and yield the result. Its
// state is {value, srcStatePtr, srcStepPtr, srcStepLen, fnPtr, fnLen, srcStateBytes}.
// Because it is itself a step cell, take_while/fold pulling it pulls the source —
// a genuinely lazy pipeline that materializes nothing.
const SysStreamMapWAT = `(module
  (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 100))
  (import "hdm:kernel/cell-dispatch" "invoke-cell" (func $invoke (param i32 i32 i32 i32) (result i32)))
  (func (export "run-tick") (param $s i32) (param $l i32) (result i32)
    (local $srcSt i32) (local $srcP i32) (local $srcL i32) (local $fnP i32) (local $fnL i32) (local $srcB i32)
    (local.set $srcSt (i32.load offset=4  (local.get $s)))
    (local.set $srcP  (i32.load offset=8  (local.get $s)))
    (local.set $srcL  (i32.load offset=12 (local.get $s)))
    (local.set $fnP   (i32.load offset=16 (local.get $s)))
    (local.set $fnL   (i32.load offset=20 (local.get $s)))
    (local.set $srcB  (i32.load offset=24 (local.get $s)))
    (if (result i32) (i32.eqz (call $invoke (local.get $srcP) (local.get $srcL) (local.get $srcSt) (local.get $srcB)))
      (then (i32.const 0))
      (else
        (i32.store (local.get $s) (call $invoke (local.get $fnP) (local.get $fnL) (local.get $srcSt) (i32.const 4)))
        (i32.const 1)))))`

package execution

import "fmt"

// Functional combinators, P1. A combinator is
// an ordinary system CELL that takes a config and applies a passed-in FUNCTION CELL
// across data via the existing cell-dispatch bridge (invoke-cell). "Passing a WAT as
// a function" is just passing the function cell's URN; the combinator calls it per
// element. This lets the model write a small imperative LEAF (e.g. escape-time for
// one point) and compose it with a correct, reusable map — instead of hand-writing a
// whole nested-loop numeric kernel.

// Combinator is a system combinator cell: its URN, WAT source, and a one-line
// semantics used as its functional intent when seeded.
type Combinator struct {
	URN    string
	WAT    string
	Intent string
}

// SystemCombinators is the standard library, seeded as Gen-0 cells at boot.
func SystemCombinators() []Combinator {
	return []Combinator{
		{SysMapURN, SysMapWAT, "map combinator: out[i]=fn(in[i]); config {fnPtr,fnLen,inPtr,outPtr,n,elemWords}"},
		{SysFoldURN, SysFoldWAT, "fold/reduce: acc=fn(acc,in[i]) from seed; config {fnPtr,fnLen,inPtr,n,elemWords,seed,argBufPtr}; fn reads [acc,elem]"},
		{SysFilterURN, SysFilterWAT, "filter: keep in[i] where pred(in[i])!=0; config {fnPtr,fnLen,inPtr,outPtr,n,elemWords}; returns kept count"},
		{SysIterateURN, SysIterateWAT, "iterate_until: apply fn to state up to maxSteps until fn returns 0; config {fnPtr,fnLen,statePtr,stateWords,maxSteps}; returns steps"},
		{SysScanURN, SysScanWAT, "scan: out[i]=acc after acc=fn(acc,in[i]) from seed; config {fnPtr,fnLen,inPtr,outPtr,n,elemWords,seed,argBufPtr}"},
		{SysZipURN, SysZipWAT, "zip: out[i]=fn(a[i],b[i]); config {fnPtr,fnLen,aPtr,bPtr,outPtr,n,elemWords,argBufPtr}; fn reads [a,b]"},
	}
}

// SysMapURN is the canonical URN of the map combinator cell.
const SysMapURN = "urn:hdm:sys:map"

const (
	SysFoldURN    = "urn:hdm:sys:fold"
	SysFilterURN  = "urn:hdm:sys:filter"
	SysIterateURN = "urn:hdm:sys:iterate"
	SysScanURN    = "urn:hdm:sys:scan"
	SysZipURN     = "urn:hdm:sys:zip"
)

// SysMapWAT is `map`: given a config, write out[i] = fn(in[i]) for i in 0..n.
//
// Config (little-endian i32 words at the run-tick arg pointer):
//
//	+0  fnPtr     pointer to the function cell's URN string (in shared memory)
//	+4  fnLen     length of that URN
//	+8  inPtr     base of the input array
//	+12 outPtr    base of the output array (one i32 per element)
//	+16 n         element count
//	+20 elemWords words per input element (stride = elemWords*4)
//
// The function cell's run-tick(argPtr, argLen) receives a pointer to in[i]
// (elemWords*4 bytes) and returns its i32 result, which map stores at out[i].
// Returns n.
const SysMapWAT = `(module
  (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 100))
  (import "hdm:kernel/cell-dispatch" "invoke-cell" (func $invoke (param i32 i32 i32 i32) (result i32)))
  (func (export "run-tick") (param $cfg i32) (param $len i32) (result i32)
    (local $fnPtr i32) (local $fnLen i32) (local $inPtr i32) (local $outPtr i32)
    (local $n i32) (local $ew i32) (local $i i32) (local $stride i32)
    (local.set $fnPtr  (i32.load offset=0  (local.get $cfg)))
    (local.set $fnLen  (i32.load offset=4  (local.get $cfg)))
    (local.set $inPtr  (i32.load offset=8  (local.get $cfg)))
    (local.set $outPtr (i32.load offset=12 (local.get $cfg)))
    (local.set $n      (i32.load offset=16 (local.get $cfg)))
    (local.set $ew     (i32.load offset=20 (local.get $cfg)))
    (local.set $stride (i32.mul (local.get $ew) (i32.const 4)))
    (local.set $i (i32.const 0))
    (block $done
      (loop $loop
        (br_if $done (i32.ge_u (local.get $i) (local.get $n)))
        (i32.store
          (i32.add (local.get $outPtr) (i32.mul (local.get $i) (i32.const 4)))
          (call $invoke
            (local.get $fnPtr) (local.get $fnLen)
            (i32.add (local.get $inPtr) (i32.mul (local.get $i) (local.get $stride)))
            (local.get $stride)))
        (local.set $i (i32.add (local.get $i) (i32.const 1)))
        (br $loop)))
    (local.get $n)))`

// SysFoldWAT is `fold`: acc = fn(acc, in[i]) for i in 0..n, from seed. fn reads its
// args from argBuf: [acc, elem words...] (1+elemWords). Returns the final acc.
// Config: {fnPtr,fnLen,inPtr,n,elemWords,seed,argBufPtr}.
const SysFoldWAT = `(module
  (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 100))
  (import "hdm:kernel/cell-dispatch" "invoke-cell" (func $invoke (param i32 i32 i32 i32) (result i32)))
  (func (export "run-tick") (param $cfg i32) (param $len i32) (result i32)
    (local $fnPtr i32) (local $fnLen i32) (local $inPtr i32) (local $n i32) (local $ew i32)
    (local $acc i32) (local $arg i32) (local $i i32) (local $stride i32) (local $k i32) (local $src i32)
    (local.set $fnPtr (i32.load offset=0 (local.get $cfg)))
    (local.set $fnLen (i32.load offset=4 (local.get $cfg)))
    (local.set $inPtr (i32.load offset=8 (local.get $cfg)))
    (local.set $n     (i32.load offset=12 (local.get $cfg)))
    (local.set $ew    (i32.load offset=16 (local.get $cfg)))
    (local.set $acc   (i32.load offset=20 (local.get $cfg)))
    (local.set $arg   (i32.load offset=24 (local.get $cfg)))
    (local.set $stride (i32.mul (local.get $ew) (i32.const 4)))
    (local.set $i (i32.const 0))
    (block $done (loop $loop
      (br_if $done (i32.ge_u (local.get $i) (local.get $n)))
      (i32.store (local.get $arg) (local.get $acc))
      (local.set $src (i32.add (local.get $inPtr) (i32.mul (local.get $i) (local.get $stride))))
      (local.set $k (i32.const 0))
      (block $cd (loop $cl
        (br_if $cd (i32.ge_u (local.get $k) (local.get $ew)))
        (i32.store
          (i32.add (i32.add (local.get $arg) (i32.const 4)) (i32.mul (local.get $k) (i32.const 4)))
          (i32.load (i32.add (local.get $src) (i32.mul (local.get $k) (i32.const 4)))))
        (local.set $k (i32.add (local.get $k) (i32.const 1)))
        (br $cl)))
      (local.set $acc (call $invoke (local.get $fnPtr) (local.get $fnLen)
        (local.get $arg) (i32.mul (i32.add (local.get $ew) (i32.const 1)) (i32.const 4))))
      (local.set $i (i32.add (local.get $i) (i32.const 1)))
      (br $loop)))
    (local.get $acc)))`

// SysFilterWAT is `filter`: compact into out[] the elements where pred(in[i])!=0.
// Returns the kept count. Config: {fnPtr,fnLen,inPtr,outPtr,n,elemWords}.
const SysFilterWAT = `(module
  (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 100))
  (import "hdm:kernel/cell-dispatch" "invoke-cell" (func $invoke (param i32 i32 i32 i32) (result i32)))
  (func (export "run-tick") (param $cfg i32) (param $len i32) (result i32)
    (local $fnPtr i32) (local $fnLen i32) (local $inPtr i32) (local $outPtr i32) (local $n i32) (local $ew i32)
    (local $i i32) (local $j i32) (local $stride i32) (local $k i32) (local $src i32) (local $dst i32)
    (local.set $fnPtr  (i32.load offset=0  (local.get $cfg)))
    (local.set $fnLen  (i32.load offset=4  (local.get $cfg)))
    (local.set $inPtr  (i32.load offset=8  (local.get $cfg)))
    (local.set $outPtr (i32.load offset=12 (local.get $cfg)))
    (local.set $n      (i32.load offset=16 (local.get $cfg)))
    (local.set $ew     (i32.load offset=20 (local.get $cfg)))
    (local.set $stride (i32.mul (local.get $ew) (i32.const 4)))
    (local.set $i (i32.const 0)) (local.set $j (i32.const 0))
    (block $done (loop $loop
      (br_if $done (i32.ge_u (local.get $i) (local.get $n)))
      (local.set $src (i32.add (local.get $inPtr) (i32.mul (local.get $i) (local.get $stride))))
      (if (call $invoke (local.get $fnPtr) (local.get $fnLen) (local.get $src) (local.get $stride))
        (then
          (local.set $dst (i32.add (local.get $outPtr) (i32.mul (local.get $j) (local.get $stride))))
          (local.set $k (i32.const 0))
          (block $cd (loop $cl
            (br_if $cd (i32.ge_u (local.get $k) (local.get $ew)))
            (i32.store
              (i32.add (local.get $dst) (i32.mul (local.get $k) (i32.const 4)))
              (i32.load (i32.add (local.get $src) (i32.mul (local.get $k) (i32.const 4)))))
            (local.set $k (i32.add (local.get $k) (i32.const 1)))
            (br $cl)))
          (local.set $j (i32.add (local.get $j) (i32.const 1)))))
      (local.set $i (i32.add (local.get $i) (i32.const 1)))
      (br $loop)))
    (local.get $j)))`

// SysIterateWAT is `iterate_until`: apply fn to state (in place) up to maxSteps,
// stopping when fn returns 0. Returns the number of steps taken. fn reads+writes
// stateWords at statePtr and returns a continue flag. Config:
// {fnPtr,fnLen,statePtr,stateWords,maxSteps}.
const SysIterateWAT = `(module
  (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 100))
  (import "hdm:kernel/cell-dispatch" "invoke-cell" (func $invoke (param i32 i32 i32 i32) (result i32)))
  (func (export "run-tick") (param $cfg i32) (param $len i32) (result i32)
    (local $fnPtr i32) (local $fnLen i32) (local $st i32) (local $sw i32) (local $max i32)
    (local $steps i32) (local $bytes i32)
    (local.set $fnPtr (i32.load offset=0  (local.get $cfg)))
    (local.set $fnLen (i32.load offset=4  (local.get $cfg)))
    (local.set $st    (i32.load offset=8  (local.get $cfg)))
    (local.set $sw    (i32.load offset=12 (local.get $cfg)))
    (local.set $max   (i32.load offset=16 (local.get $cfg)))
    (local.set $bytes (i32.mul (local.get $sw) (i32.const 4)))
    (local.set $steps (i32.const 0))
    (block $done (loop $loop
      (br_if $done (i32.ge_u (local.get $steps) (local.get $max)))
      (local.set $steps (i32.add (local.get $steps) (i32.const 1)))
      (br_if $done (i32.eqz (call $invoke (local.get $fnPtr) (local.get $fnLen) (local.get $st) (local.get $bytes))))
      (br $loop)))
    (local.get $steps)))`

// SysScanWAT is `scan`: like fold but writes each running acc to out[i]. Config:
// {fnPtr,fnLen,inPtr,outPtr,n,elemWords,seed,argBufPtr}. fn reads [acc, elem].
const SysScanWAT = `(module
  (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 100))
  (import "hdm:kernel/cell-dispatch" "invoke-cell" (func $invoke (param i32 i32 i32 i32) (result i32)))
  (func (export "run-tick") (param $cfg i32) (param $len i32) (result i32)
    (local $fnPtr i32) (local $fnLen i32) (local $inPtr i32) (local $outPtr i32) (local $n i32) (local $ew i32)
    (local $acc i32) (local $arg i32) (local $i i32) (local $stride i32) (local $k i32) (local $src i32)
    (local.set $fnPtr  (i32.load offset=0  (local.get $cfg)))
    (local.set $fnLen  (i32.load offset=4  (local.get $cfg)))
    (local.set $inPtr  (i32.load offset=8  (local.get $cfg)))
    (local.set $outPtr (i32.load offset=12 (local.get $cfg)))
    (local.set $n      (i32.load offset=16 (local.get $cfg)))
    (local.set $ew     (i32.load offset=20 (local.get $cfg)))
    (local.set $acc    (i32.load offset=24 (local.get $cfg)))
    (local.set $arg    (i32.load offset=28 (local.get $cfg)))
    (local.set $stride (i32.mul (local.get $ew) (i32.const 4)))
    (local.set $i (i32.const 0))
    (block $done (loop $loop
      (br_if $done (i32.ge_u (local.get $i) (local.get $n)))
      (i32.store (local.get $arg) (local.get $acc))
      (local.set $src (i32.add (local.get $inPtr) (i32.mul (local.get $i) (local.get $stride))))
      (local.set $k (i32.const 0))
      (block $cd (loop $cl
        (br_if $cd (i32.ge_u (local.get $k) (local.get $ew)))
        (i32.store
          (i32.add (i32.add (local.get $arg) (i32.const 4)) (i32.mul (local.get $k) (i32.const 4)))
          (i32.load (i32.add (local.get $src) (i32.mul (local.get $k) (i32.const 4)))))
        (local.set $k (i32.add (local.get $k) (i32.const 1)))
        (br $cl)))
      (local.set $acc (call $invoke (local.get $fnPtr) (local.get $fnLen)
        (local.get $arg) (i32.mul (i32.add (local.get $ew) (i32.const 1)) (i32.const 4))))
      (i32.store (i32.add (local.get $outPtr) (i32.mul (local.get $i) (i32.const 4))) (local.get $acc))
      (local.set $i (i32.add (local.get $i) (i32.const 1)))
      (br $loop)))
    (local.get $n)))`

// SysZipWAT is `zip`: out[i]=fn(a[i],b[i]). fn reads argBuf [a words..., b words...]
// (2*elemWords). Config: {fnPtr,fnLen,aPtr,bPtr,outPtr,n,elemWords,argBufPtr}.
const SysZipWAT = `(module
  (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 100))
  (import "hdm:kernel/cell-dispatch" "invoke-cell" (func $invoke (param i32 i32 i32 i32) (result i32)))
  (func (export "run-tick") (param $cfg i32) (param $len i32) (result i32)
    (local $fnPtr i32) (local $fnLen i32) (local $aPtr i32) (local $bPtr i32) (local $outPtr i32)
    (local $n i32) (local $ew i32) (local $arg i32) (local $i i32) (local $stride i32) (local $k i32)
    (local.set $fnPtr  (i32.load offset=0  (local.get $cfg)))
    (local.set $fnLen  (i32.load offset=4  (local.get $cfg)))
    (local.set $aPtr   (i32.load offset=8  (local.get $cfg)))
    (local.set $bPtr   (i32.load offset=12 (local.get $cfg)))
    (local.set $outPtr (i32.load offset=16 (local.get $cfg)))
    (local.set $n      (i32.load offset=20 (local.get $cfg)))
    (local.set $ew     (i32.load offset=24 (local.get $cfg)))
    (local.set $arg    (i32.load offset=28 (local.get $cfg)))
    (local.set $stride (i32.mul (local.get $ew) (i32.const 4)))
    (local.set $i (i32.const 0))
    (block $done (loop $loop
      (br_if $done (i32.ge_u (local.get $i) (local.get $n)))
      (local.set $k (i32.const 0))
      (block $cd (loop $cl
        (br_if $cd (i32.ge_u (local.get $k) (local.get $ew)))
        (i32.store
          (i32.add (local.get $arg) (i32.mul (local.get $k) (i32.const 4)))
          (i32.load (i32.add (i32.add (local.get $aPtr) (i32.mul (local.get $i) (local.get $stride))) (i32.mul (local.get $k) (i32.const 4)))))
        (i32.store
          (i32.add (i32.add (local.get $arg) (local.get $stride)) (i32.mul (local.get $k) (i32.const 4)))
          (i32.load (i32.add (i32.add (local.get $bPtr) (i32.mul (local.get $i) (local.get $stride))) (i32.mul (local.get $k) (i32.const 4)))))
        (local.set $k (i32.add (local.get $k) (i32.const 1)))
        (br $cl)))
      (i32.store (i32.add (local.get $outPtr) (i32.mul (local.get $i) (i32.const 4)))
        (call $invoke (local.get $fnPtr) (local.get $fnLen) (local.get $arg) (i32.mul (local.get $stride) (i32.const 2))))
      (local.set $i (i32.add (local.get $i) (i32.const 1)))
      (br $loop)))
    (local.get $n)))`

// driverBase is the scratch region a generated composition driver uses for the
// combinator config + the embedded URN strings — high in the sandbox, clear of the
// contract arrays at the region base.
const driverBase = 0xBF000

// GenerateMapDriver emits a DRIVER cell (WAT) that runs map(leaf) over the in→out
// arrays: on run-tick it writes the map config and invoke-cells sys:map. HDM
// GENERATES this boilerplate deterministically from a composition spec, so the model
// only writes the small leaf. inOff/outOff are the contract array offsets, n the
// element count, elemWords the input element width.
func GenerateMapDriver(leafURN string, inOff, outOff, n, elemWords int) string {
	cfg := driverBase
	mapOff := cfg + 24
	leafOff := mapOff + len(SysMapURN)
	return fmt.Sprintf(`(module
  (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 100))
  (import "hdm:kernel/cell-dispatch" "invoke-cell" (func $invoke (param i32 i32 i32 i32) (result i32)))
  (data (i32.const %d) %q)
  (data (i32.const %d) %q)
  (func (export "run-tick") (param i32 i32) (result i32)
    (i32.store (i32.const %d) (i32.const %d))   ;; fnPtr = leaf urn
    (i32.store (i32.const %d) (i32.const %d))   ;; fnLen
    (i32.store (i32.const %d) (i32.const %d))   ;; inPtr
    (i32.store (i32.const %d) (i32.const %d))   ;; outPtr
    (i32.store (i32.const %d) (i32.const %d))   ;; n
    (i32.store (i32.const %d) (i32.const %d))   ;; elemWords
    (call $invoke (i32.const %d) (i32.const %d) (i32.const %d) (i32.const 24))))`,
		mapOff, SysMapURN,
		leafOff, leafURN,
		cfg+0, leafOff,
		cfg+4, len(leafURN),
		cfg+8, inOff,
		cfg+12, outOff,
		cfg+16, n,
		cfg+20, elemWords,
		mapOff, len(SysMapURN), cfg)
}

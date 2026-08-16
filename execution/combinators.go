package execution

import "github.com/mdcfrancis/flow/stdlib"

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
var SysMapWAT = stdlib.MustCell("map")

// SysFoldWAT is `fold`: acc = fn(acc, in[i]) for i in 0..n, from seed. fn reads its
// args from argBuf: [acc, elem words...] (1+elemWords). Returns the final acc.
// Config: {fnPtr,fnLen,inPtr,n,elemWords,seed,argBufPtr}.
var SysFoldWAT = stdlib.MustCell("fold")

// SysFilterWAT is `filter`: compact into out[] the elements where pred(in[i])!=0.
// Returns the kept count. Config: {fnPtr,fnLen,inPtr,outPtr,n,elemWords}.
var SysFilterWAT = stdlib.MustCell("filter")

// SysIterateWAT is `iterate_until`: apply fn to state (in place) up to maxSteps,
// stopping when fn returns 0. Returns the number of steps taken. fn reads+writes
// stateWords at statePtr and returns a continue flag. Config:
// {fnPtr,fnLen,statePtr,stateWords,maxSteps}.
var SysIterateWAT = stdlib.MustCell("iterate")

// SysScanWAT is `scan`: like fold but writes each running acc to out[i]. Config:
// {fnPtr,fnLen,inPtr,outPtr,n,elemWords,seed,argBufPtr}. fn reads [acc, elem].
var SysScanWAT = stdlib.MustCell("scan")

// SysZipWAT is `zip`: out[i]=fn(a[i],b[i]). fn reads argBuf [a words..., b words...]
// (2*elemWords). Config: {fnPtr,fnLen,aPtr,bPtr,outPtr,n,elemWords,argBufPtr}.
var SysZipWAT = stdlib.MustCell("zip")

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
	return stdlib.MustTemplate("map-driver", map[string]any{
		"MapOff": mapOff, "MapURN": SysMapURN, "MapLen": len(SysMapURN),
		"LeafOff": leafOff, "LeafURN": leafURN, "LeafLen": len(leafURN),
		"CfgFnPtr": cfg + 0, "CfgFnLen": cfg + 4, "CfgInPtr": cfg + 8,
		"CfgOutPtr": cfg + 12, "CfgN": cfg + 16, "CfgElemWords": cfg + 20,
		"InOff": inOff, "OutOff": outOff, "N": n, "ElemWords": elemWords,
		"CfgBase": cfg,
	})
}

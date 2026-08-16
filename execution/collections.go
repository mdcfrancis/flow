package execution

import "github.com/mdcfrancis/flow/stdlib"

// sys:list and sys:set — two more private-page primitives, same shape as sys:dict: pure ops
// over the private window (windowBase = 0x00400000), one run-tick with an opcode in args, state
// hidden behind a handle. Both are enrolled as evolvable cells with differential acceptance.

// --- sys:list — a growable array of u32 over the private window --------------------------------
//
// Layout: window[0] = length; element i at window[4 + i*4]. Ops (arg [op, x] at argPtr):
//
//	op 0 = LEN         -> returns length
//	op 1 = APPEND(x)   -> stores x at the end, returns the new length (0 if full)
//	op 2 = GET(idx=x)  -> returns element idx, or 0 if out of range

// SysListURN is the canonical URN of the list primitive cell.
const SysListURN = "urn:hdm:sys:list"

// ListCap is the max element count (window is 64 KiB; header 4 B + ListCap*4 stays inside).
const ListCap = 16000

// SysListWAT implements len/append/get over the private window.
var SysListWAT = stdlib.MustCell("list")

// --- sys:set — membership of u32 keys over the private window ----------------------------------
//
// Layout: slot i (a single u32 key) at window + i*4; key 0 = empty, so valid keys are >= 1.
// Open addressing, linear probing. Ops (arg [op, key] at argPtr):
//
//	op 0 = CONTAINS(key) -> 1 if present, else 0
//	op 1 = ADD(key)      -> inserts (idempotent), returns 1

// SysSetURN is the canonical URN of the set primitive cell.
const SysSetURN = "urn:hdm:sys:set"

// SetCap is the fixed slot count.
const SetCap = 512

// SysSetWAT implements contains/add over the private window via linear probing.
var SysSetWAT = stdlib.MustCell("set")

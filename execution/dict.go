package execution

import "github.com/mdcfrancis/flow/stdlib"

// sys:dict — the first PRIVATE-PAGE primitive. An associative uint32→uint32 map that is
// pure ops over a private page (no cell-owned state): the runtime maps the dict's page into
// the window, and the WAT treats windowBase as a flat array of slots. Because the backing
// store is a private page, its representation (open-addressing buckets) is invisible to every
// other cell — the public surface is just get/insert through this one dispatched cell.
//
// A caller (or the host, for acceptance) selects which dict instance is live by mapping its
// page before dispatch (MapPage). Ops go through the single run-tick entry with an opcode:
//
//	arg buffer (little-endian i32 at argPtr): +0 op, +4 key, +8 val
//	  op 0 = GET    -> returns val for key, or 0 if absent
//	  op 1 = INSERT -> stores key=val (updating in place), returns 1
//
// Layout in the window: slot i at windowBase + i*8 = {u32 key, u32 val}. Key 0 marks an EMPTY
// slot (a freshly paged-in page is all zeros = all empty), so valid keys are >= 1. Collisions
// are resolved by linear probing. Capacity is DictCap slots; callers stay well under it.

// SysDictURN is the canonical URN of the dict primitive cell.
const SysDictURN = "urn:hdm:sys:dict"

// DictCap is the fixed slot count (windowBase..+DictCap*8 stays well inside the 64 KiB window).
const DictCap = 512

// DictWindowBase is where the dict's page maps (= windowBase). Exported so the acceptance
// suite can seed dict slots at the right offset; the two must not drift.
const DictWindowBase = windowBase

// SysDictWAT implements get/insert over the private window via linear probing.
var SysDictWAT = stdlib.MustCell("dict")

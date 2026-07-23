package execution

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
const SysDictWAT = `(module
  (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 100))
  (func (export "run-tick") (param $arg i32) (param $len i32) (result i32)
    (local $op i32) (local $key i32) (local $val i32)
    (local $i i32) (local $addr i32) (local $k i32)
    (local.set $op  (i32.load offset=0 (local.get $arg)))
    (local.set $key (i32.load offset=4 (local.get $arg)))
    (local.set $val (i32.load offset=8 (local.get $arg)))
    (local.set $i (i32.rem_u (local.get $key) (i32.const 512)))
    (if (i32.eq (local.get $op) (i32.const 1))
      (then
        (block $done
          (loop $probe
            (local.set $addr (i32.add (i32.const 0x00400000) (i32.mul (local.get $i) (i32.const 8))))
            (local.set $k (i32.load (local.get $addr)))
            (if (i32.or (i32.eqz (local.get $k)) (i32.eq (local.get $k) (local.get $key)))
              (then
                (i32.store offset=0 (local.get $addr) (local.get $key))
                (i32.store offset=4 (local.get $addr) (local.get $val))
                (br $done)))
            (local.set $i (i32.rem_u (i32.add (local.get $i) (i32.const 1)) (i32.const 512)))
            (br $probe)))
        (return (i32.const 1)))
      (else
        (block $done
          (loop $probe
            (local.set $addr (i32.add (i32.const 0x00400000) (i32.mul (local.get $i) (i32.const 8))))
            (local.set $k (i32.load (local.get $addr)))
            (if (i32.eqz (local.get $k)) (then (return (i32.const 0))))
            (if (i32.eq (local.get $k) (local.get $key))
              (then (return (i32.load offset=4 (local.get $addr)))))
            (local.set $i (i32.rem_u (i32.add (local.get $i) (i32.const 1)) (i32.const 512)))
            (br $probe)))
        (return (i32.const 0))))
    (i32.const 0)))`

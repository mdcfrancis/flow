package execution

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
const SysListWAT = `(module
  (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 100))
  (func (export "run-tick") (param $arg i32) (param $len i32) (result i32)
    (local $op i32) (local $x i32) (local $n i32)
    (local.set $op (i32.load offset=0 (local.get $arg)))
    (local.set $x  (i32.load offset=4 (local.get $arg)))
    (local.set $n  (i32.load (i32.const 0x00400000)))
    (if (i32.eq (local.get $op) (i32.const 1))
      (then
        (if (i32.ge_u (local.get $n) (i32.const 16000)) (then (return (i32.const 0))))
        (i32.store (i32.add (i32.const 0x00400004) (i32.mul (local.get $n) (i32.const 4))) (local.get $x))
        (i32.store (i32.const 0x00400000) (i32.add (local.get $n) (i32.const 1)))
        (return (i32.add (local.get $n) (i32.const 1)))))
    (if (i32.eq (local.get $op) (i32.const 2))
      (then
        (if (i32.lt_u (local.get $x) (local.get $n))
          (then (return (i32.load (i32.add (i32.const 0x00400004) (i32.mul (local.get $x) (i32.const 4)))))))
        (return (i32.const 0))))
    (local.get $n)))`

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
const SysSetWAT = `(module
  (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 100))
  (func (export "run-tick") (param $arg i32) (param $len i32) (result i32)
    (local $op i32) (local $key i32) (local $i i32) (local $addr i32) (local $k i32)
    (local.set $op  (i32.load offset=0 (local.get $arg)))
    (local.set $key (i32.load offset=4 (local.get $arg)))
    (local.set $i (i32.rem_u (local.get $key) (i32.const 512)))
    (if (i32.eq (local.get $op) (i32.const 1))
      (then
        (block $done
          (loop $probe
            (local.set $addr (i32.add (i32.const 0x00400000) (i32.mul (local.get $i) (i32.const 4))))
            (local.set $k (i32.load (local.get $addr)))
            (if (i32.or (i32.eqz (local.get $k)) (i32.eq (local.get $k) (local.get $key)))
              (then (i32.store (local.get $addr) (local.get $key)) (br $done)))
            (local.set $i (i32.rem_u (i32.add (local.get $i) (i32.const 1)) (i32.const 512)))
            (br $probe)))
        (return (i32.const 1))))
    (block $done
      (loop $probe
        (local.set $addr (i32.add (i32.const 0x00400000) (i32.mul (local.get $i) (i32.const 4))))
        (local.set $k (i32.load (local.get $addr)))
        (if (i32.eqz (local.get $k)) (then (return (i32.const 0))))
        (if (i32.eq (local.get $k) (local.get $key)) (then (return (i32.const 1))))
        (local.set $i (i32.rem_u (i32.add (local.get $i) (i32.const 1)) (i32.const 512)))
        (br $probe)))
    (i32.const 0)))`

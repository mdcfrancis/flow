(module
  (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 100))
  (func (export "run-tick") (param $p i32) (param $len i32) (result i32)
    (local $nops i32) (local $k i32) (local $op i32) (local $arg i32)
    (local $head i32) (local $tail i32) (local $acc i32) (local $val i32)
    (local.set $nops (i32.load (i32.const 0x00010000)))
    (local.set $head (i32.const 0)) (local.set $tail (i32.const 0)) (local.set $acc (i32.const 0))
    (local.set $k (i32.const 0))
    (block $done (loop $loop
      (br_if $done (i32.ge_u (local.get $k) (local.get $nops)))
      (local.set $op  (i32.load (i32.add (i32.const 0x00010004) (i32.mul (local.get $k) (i32.const 8)))))
      (local.set $arg (i32.load (i32.add (i32.const 0x00010008) (i32.mul (local.get $k) (i32.const 8)))))
      (if (i32.eq (local.get $op) (i32.const 1))
        (then
          (i32.store (i32.add (i32.const 0x00400040) (i32.mul (local.get $tail) (i32.const 4))) (local.get $arg))
          (local.set $tail (i32.and (i32.add (local.get $tail) (i32.const 1)) (i32.const 15))))
        (else
          (local.set $val (i32.load (i32.add (i32.const 0x00400040) (i32.mul (local.get $head) (i32.const 4)))))
          (local.set $head (i32.and (i32.add (local.get $head) (i32.const 1)) (i32.const 15)))
          (local.set $acc (i32.add (i32.mul (local.get $acc) (i32.const 2)) (local.get $val)))))
      (local.set $k (i32.add (local.get $k) (i32.const 1)))
      (br $loop)))
    (local.get $acc)))

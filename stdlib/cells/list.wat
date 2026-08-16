(module
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
    (local.get $n)))

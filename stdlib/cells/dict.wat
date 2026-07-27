(module
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
    (i32.const 0)))

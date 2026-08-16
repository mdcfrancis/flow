(module
  (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 100))
  (func (export "run-tick") (param $p i32) (param $len i32) (result i32)
    (i32.load (local.get $p))))

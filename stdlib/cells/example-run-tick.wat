(module
  (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 100))
  (func (export "run-tick") (param $ptr i32) (param $len i32) (result i32)
    local.get $ptr i32.load8_u))

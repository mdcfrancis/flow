(module
  (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 1))
  (func (export "run-tick") (param i32 i32) (result i32)
    (i32.gt_s (i32.load (local.get 0)) (i32.const 0))))

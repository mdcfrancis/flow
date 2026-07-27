(module
  (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 100))
  (func (export "render-frame") (param $base i32) (param $cap i32) (result i32)
    local.get $base i32.const 1 i32.store
    local.get $base i32.const 4 i32.add i32.const 20 i32.store
    local.get $base i32.const 8 i32.add i32.const 20 i32.store
    local.get $base i32.const 12 i32.add i32.const 80 i32.store
    local.get $base i32.const 16 i32.add i32.const 80 i32.store
    local.get $base i32.const 20 i32.add i32.const 0x3A6EA5FF i32.store
    i32.const 24))

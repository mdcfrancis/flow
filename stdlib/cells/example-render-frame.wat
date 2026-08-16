(module
  (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 100))
  (func (export "render-frame") (param $base i32) (param $cap i32) (result i32)
    local.get $base i32.const 257 i32.store
    local.get $base i32.const 4 i32.add i32.const 20 i32.store
    local.get $base i32.const 8 i32.add i32.const 30 i32.store
    local.get $base i32.const 12 i32.add i32.const 8 i32.store
    local.get $base i32.const 16 i32.add i32.const 8 i32.store
    local.get $base i32.const 20 i32.add i32.const 0x33FF66FF i32.store
    i32.const 24))

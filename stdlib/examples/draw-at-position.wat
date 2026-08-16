;; kind: render
;; entry: render-frame
;; semantics: read ball_x and ball_y and draw a filled circle at that position
;; reads: ball_x, ball_y
;; tags: draw-at-position
(module
  (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 100))
  (func (export "render-frame") (param $base i32) (param $cap i32) (result i32)
    local.get $base i32.const 3 i32.store
    local.get $base i32.const 4 i32.add i32.const 0xB0000 i32.load i32.store
    local.get $base i32.const 8 i32.add i32.const 0xB0004 i32.load i32.store
    local.get $base i32.const 12 i32.add i32.const 8 i32.store
    local.get $base i32.const 16 i32.add i32.const 0 i32.store
    local.get $base i32.const 20 i32.add i32.const 0xFFCC33FF i32.store
    i32.const 24))

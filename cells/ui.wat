(module
  ;; Vectorized Canvas UI cell. On render-frame it emits a vector draw stream
  ;; into the Canvas region (base passed as $base, currently 0x51000) and
  ;; returns the byte length.
  ;; Stream format: fixed 24-byte records [op, a, b, c, d, rgba] little-endian.
  ;;   op 1 = rect(x=a, y=b, w=c, h=d), op 2 = line(x1=a,y1=b,x2=c,y2=d),
  ;;   op 3 = circle(cx=a, cy=b, r=c). rgba packed 0xRRGGBBAA.
  (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 100))
  (func (export "render-frame") (param $base i32) (param $cap i32) (result i32)
    ;; record 0: dark background rectangle covering the 320x240 canvas
    (i32.store (i32.add (local.get $base) (i32.const 0)) (i32.const 1))
    (i32.store (i32.add (local.get $base) (i32.const 4)) (i32.const 0))
    (i32.store (i32.add (local.get $base) (i32.const 8)) (i32.const 0))
    (i32.store (i32.add (local.get $base) (i32.const 12)) (i32.const 320))
    (i32.store (i32.add (local.get $base) (i32.const 16)) (i32.const 240))
    (i32.store (i32.add (local.get $base) (i32.const 20)) (i32.const 0x202833FF))
    ;; record 1: red rectangle
    (i32.store (i32.add (local.get $base) (i32.const 24)) (i32.const 1))
    (i32.store (i32.add (local.get $base) (i32.const 28)) (i32.const 40))
    (i32.store (i32.add (local.get $base) (i32.const 32)) (i32.const 40))
    (i32.store (i32.add (local.get $base) (i32.const 36)) (i32.const 110))
    (i32.store (i32.add (local.get $base) (i32.const 40)) (i32.const 70))
    (i32.store (i32.add (local.get $base) (i32.const 44)) (i32.const 0xE24A4AFF))
    ;; record 2: green rectangle
    (i32.store (i32.add (local.get $base) (i32.const 48)) (i32.const 1))
    (i32.store (i32.add (local.get $base) (i32.const 52)) (i32.const 170))
    (i32.store (i32.add (local.get $base) (i32.const 56)) (i32.const 120))
    (i32.store (i32.add (local.get $base) (i32.const 60)) (i32.const 110))
    (i32.store (i32.add (local.get $base) (i32.const 64)) (i32.const 70))
    (i32.store (i32.add (local.get $base) (i32.const 68)) (i32.const 0x4AE27AFF))
    ;; record 3: white diagonal line
    (i32.store (i32.add (local.get $base) (i32.const 72)) (i32.const 2))
    (i32.store (i32.add (local.get $base) (i32.const 76)) (i32.const 0))
    (i32.store (i32.add (local.get $base) (i32.const 80)) (i32.const 0))
    (i32.store (i32.add (local.get $base) (i32.const 84)) (i32.const 320))
    (i32.store (i32.add (local.get $base) (i32.const 88)) (i32.const 240))
    (i32.store (i32.add (local.get $base) (i32.const 92)) (i32.const 0xFFFFFFFF))
    ;; 4 records * 24 bytes
    i32.const 96))

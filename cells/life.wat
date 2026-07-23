(module
  ;; Conway's Game of Life as a Canvas UI cell. Grid state lives in shared memory
  ;; (persists across render-frame ticks): current gen at 0x90000, scratch next
  ;; gen at 0x94000, an init sentinel at 0x9F000. Each render-frame seeds on the
  ;; first tick, then steps one generation and emits a rect per live cell.
  ;; Grid 40x30 on a 320x240 canvas (8px cells).
  (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 100))

  ;; get(x,y): live bit at (x,y), or 0 when out of bounds.
  (func $get (param $x i32) (param $y i32) (result i32)
    local.get $x i32.const 0 i32.lt_s
    local.get $x i32.const 40 i32.ge_s i32.or
    local.get $y i32.const 0 i32.lt_s i32.or
    local.get $y i32.const 30 i32.ge_s i32.or
    if (result i32)
      i32.const 0
    else
      i32.const 589824
      local.get $y i32.const 40 i32.mul i32.add
      local.get $x i32.add
      i32.load8_u
    end)

  ;; seed(): deterministic pseudo-random field (~50% density).
  (func $seed
    (local $i i32)
    i32.const 0 local.set $i
    block $e loop $l
      local.get $i i32.const 1200 i32.ge_s br_if $e
      i32.const 589824 local.get $i i32.add
      local.get $i i32.const 1103515245 i32.mul i32.const 12345 i32.add
      i32.const 16 i32.shr_u i32.const 1 i32.and
      i32.store8
      local.get $i i32.const 1 i32.add local.set $i br $l
    end end)

  ;; step(): compute next generation into scratch, then copy back.
  (func $step
    (local $x i32) (local $y i32) (local $c i32) (local $a i32) (local $i i32)
    i32.const 0 local.set $y
    block $ye loop $yl
      local.get $y i32.const 30 i32.ge_s br_if $ye
      i32.const 0 local.set $x
      block $xe loop $xl
        local.get $x i32.const 40 i32.ge_s br_if $xe
        local.get $x i32.const 1 i32.sub local.get $y i32.const 1 i32.sub call $get
        local.get $x local.get $y i32.const 1 i32.sub call $get i32.add
        local.get $x i32.const 1 i32.add local.get $y i32.const 1 i32.sub call $get i32.add
        local.get $x i32.const 1 i32.sub local.get $y call $get i32.add
        local.get $x i32.const 1 i32.add local.get $y call $get i32.add
        local.get $x i32.const 1 i32.sub local.get $y i32.const 1 i32.add call $get i32.add
        local.get $x local.get $y i32.const 1 i32.add call $get i32.add
        local.get $x i32.const 1 i32.add local.get $y i32.const 1 i32.add call $get i32.add
        local.set $c
        local.get $x local.get $y call $get local.set $a
        ;; next = (c==3) | (alive & c==2)
        i32.const 606208 local.get $y i32.const 40 i32.mul i32.add local.get $x i32.add
        local.get $c i32.const 3 i32.eq
        local.get $a i32.const 1 i32.eq local.get $c i32.const 2 i32.eq i32.and
        i32.or
        i32.store8
        local.get $x i32.const 1 i32.add local.set $x br $xl
      end end
      local.get $y i32.const 1 i32.add local.set $y br $yl
    end end
    ;; copy scratch -> current
    i32.const 0 local.set $i
    block $ce loop $cl
      local.get $i i32.const 1200 i32.ge_s br_if $ce
      i32.const 589824 local.get $i i32.add
      i32.const 606208 local.get $i i32.add i32.load8_u
      i32.store8
      local.get $i i32.const 1 i32.add local.set $i br $cl
    end end)

  ;; set(gx,gy): force a grid cell alive (bounds-checked).
  (func $set (param $gx i32) (param $gy i32)
    local.get $gx i32.const 0 i32.lt_s
    local.get $gx i32.const 40 i32.ge_s i32.or
    local.get $gy i32.const 0 i32.lt_s i32.or
    local.get $gy i32.const 30 i32.ge_s i32.or
    if return end
    i32.const 589824 local.get $gy i32.const 40 i32.mul i32.add local.get $gx i32.add
    i32.const 1 i32.store8)

  ;; inject(): consume the newest HMI discrete event. On a fresh click/mousedown
  ;; (event seq at 0x50010 differs from the last one we processed, stashed at
  ;; 0x9F004), drop a 2x2 live block at the clicked grid cell. Event x/y are
  ;; canvas pixels (0x50018/0x5001C); /8 maps them onto the 40x30 grid.
  (func $inject
    (local $seq i32) (local $et i32) (local $gx i32) (local $gy i32)
    i32.const 327696 i32.load local.set $seq          ;; 0x50010 InEventSeq
    local.get $seq i32.eqz if return end              ;; nothing latched yet
    local.get $seq i32.const 651268 i32.load i32.eq
    if return end                                     ;; already consumed
    i32.const 651268 local.get $seq i32.store         ;; 0x9F004 mark consumed
    i32.const 327700 i32.load local.set $et           ;; 0x50014 InEventType
    local.get $et i32.const 4 i32.eq                  ;; click
    local.get $et i32.const 2 i32.eq i32.or           ;; or mousedown
    i32.eqz if return end
    i32.const 327704 i32.load i32.const 8 i32.div_s local.set $gx  ;; 0x50018 x/8
    i32.const 327708 i32.load i32.const 8 i32.div_s local.set $gy  ;; 0x5001C y/8
    local.get $gx local.get $gy call $set
    local.get $gx i32.const 1 i32.add local.get $gy call $set
    local.get $gx local.get $gy i32.const 1 i32.add call $set
    local.get $gx i32.const 1 i32.add local.get $gy i32.const 1 i32.add call $set)

  (func (export "render-frame") (param $base i32) (param $cap i32) (result i32)
    (local $p i32) (local $x i32) (local $y i32) (local $recs i32)
    ;; first tick seeds; later ticks step.
    i32.const 651264 i32.load i32.const 305419896 i32.ne
    if
      call $seed
      i32.const 651264 i32.const 305419896 i32.store
    else
      call $step
    end
    ;; fold in any freshly clicked cells (Peripheral Input Gateway)
    call $inject
    ;; background record (layer 0: static basemap)
    local.get $base local.set $p
    local.get $p i32.const 1 i32.store
    local.get $p i32.const 4 i32.add i32.const 0 i32.store
    local.get $p i32.const 8 i32.add i32.const 0 i32.store
    local.get $p i32.const 12 i32.add i32.const 320 i32.store
    local.get $p i32.const 16 i32.add i32.const 240 i32.store
    local.get $p i32.const 20 i32.add i32.const 0x101822FF i32.store
    local.get $p i32.const 24 i32.add local.set $p
    i32.const 1 local.set $recs
    ;; one rect per live cell
    i32.const 0 local.set $y
    block $ye loop $yl
      local.get $y i32.const 30 i32.ge_s br_if $ye
      i32.const 0 local.set $x
      block $xe loop $xl
        local.get $x i32.const 40 i32.ge_s br_if $xe
        local.get $x local.get $y call $get
        if
          local.get $p i32.const 257 i32.store ;; op=(layer 1<<8)|rect
          local.get $p i32.const 4 i32.add local.get $x i32.const 8 i32.mul i32.store
          local.get $p i32.const 8 i32.add local.get $y i32.const 8 i32.mul i32.store
          local.get $p i32.const 12 i32.add i32.const 7 i32.store
          local.get $p i32.const 16 i32.add i32.const 7 i32.store
          local.get $p i32.const 20 i32.add i32.const 0x5AE39AFF i32.store
          local.get $p i32.const 24 i32.add local.set $p
          local.get $recs i32.const 1 i32.add local.set $recs
        end
        local.get $x i32.const 1 i32.add local.set $x br $xl
      end end
      local.get $y i32.const 1 i32.add local.set $y br $yl
    end end
    local.get $recs i32.const 24 i32.mul)
)

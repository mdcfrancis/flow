(module
  ;; A cell with input-dependent control flow, so different transactions exercise
  ;; different Discovery Invariants: byte 0 traps (Fault Mitigation), the low/high
  ;; branches enter distinct helper functions (Code-Branch Exploration), and the
  ;; returned byte spans a scalar range (Boundary Extremum).
  (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 100))

  (func $branch_low (result i32)
    i32.const 100 i32.const 1 i32.add drop
    i32.const 0)
  (func $branch_high (result i32)
    i32.const 200 i32.const 1 i32.add drop
    i32.const 0)

  (func (export "run-tick") (param $p i32) (param $len i32) (result i32)
    (local $b i32)
    local.get $p i32.load8_u
    local.set $b

    ;; Fault path: a zero byte traps.
    local.get $b i32.eqz
    if
      unreachable
    end

    ;; Distinct branches enter distinct functions (coverage differs by input).
    local.get $b i32.const 128 i32.lt_u
    if
      call $branch_low drop
    else
      call $branch_high drop
    end

    ;; Return the input byte: a scalar that varies across transactions.
    local.get $b))

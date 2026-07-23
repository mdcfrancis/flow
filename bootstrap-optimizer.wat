(module
  ;; Import the shared cluster memory buffer (100 pages = 6.4MB).
  (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 100))

  ;; Host cognitive engine: deterministic reasoning invocation.
  ;; Takes (sys_ptr, sys_len, ctx_ptr, ctx_len) and returns (resp_ptr, resp_len).
  (import "hdm:kernel/cognitive-engine" "invoke-reasoning"
    (func $invoke_reasoning
      (param i32 i32 i32 i32)
      (result i32 i32)))

  ;; Redundant helper cells: work the tick does not need. A correct optimizer
  ;; can delete these calls (lowering fuel) without changing the result, but it
  ;; must NOT drop the essential $invoke_reasoning call above (enforced as an
  ;; effectful structural invariant by the orchestrator).
  (func $spin1 (result i32)
    i32.const 7 i32.const 3 i32.mul drop
    i32.const 0)
  (func $spin2 (result i32)
    i32.const 11 i32.const 5 i32.add drop
    i32.const 0)
  (func $spin3 (result i32)
    i32.const 99 i32.popcnt drop
    i32.const 0)

  ;; Exported entry point called by the Go scheduler.
  (func (export "run-tick")
    (param $target_urn_ptr i32) (param $target_urn_len i32)
    (result i32)

    (local $status_code i32)

    ;; Fire the reasoning request across the host boundary (essential).
    ;; Offset 0: system prompt (length 60); offset 64: target URN (length 21).
    i32.const 0
    i32.const 60
    i32.const 64
    i32.const 21
    call $invoke_reasoning
    drop
    drop

    ;; Redundant work the optimizer is expected to trim.
    call $spin1 drop
    call $spin2 drop
    call $spin3 drop

    ;; Return the success marker.
    i32.const 1)

  ;; Static system prompt at offset 0.
  (data (i32.const 0) "SYSTEM ROLE: HDM REFACTORING CORE. LOWER METERED SYSTEM ENERGY")
  ;; Target cell URN at offset 64.
  (data (i32.const 64) "urn:hdm:sys:optimizer"))

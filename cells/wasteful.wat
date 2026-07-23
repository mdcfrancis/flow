(module
  ;; A deliberately wasteful cell: run-tick calls three redundant helper cells
  ;; that compute values it immediately discards. Each call is a function-
  ;; boundary crossing that costs fuel, so a correct optimizer can delete them
  ;; without changing the result (always 1) or touching memory — a clear
  ;; opportunity for the evolutionary loop to lower the Hamiltonian energy.
  (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 100))

  (func $spin1 (result i32)
    i32.const 7 i32.const 3 i32.mul drop
    i32.const 0)
  (func $spin2 (result i32)
    i32.const 11 i32.const 5 i32.add drop
    i32.const 0)
  (func $spin3 (result i32)
    i32.const 99 i32.popcnt drop
    i32.const 0)

  (func (export "run-tick") (param i32 i32) (result i32)
    call $spin1 drop
    call $spin2 drop
    call $spin3 drop
    i32.const 1))

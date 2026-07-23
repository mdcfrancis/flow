(module
  ;; Edge router cell: the Gateway writes the inbound packet into the packet-frame
  ;; region and register-jumps here with (ptr, len). This crude router returns the
  ;; packet length as its status; the optimizer/annealer can evolve real routing.
  (import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 100))
  (func (export "route-packet") (param $ptr i32) (param $len i32) (result i32)
    local.get $len))

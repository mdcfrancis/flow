package appgen

import (
	"strings"
	"testing"

	"github.com/mdcfrancis/flow/flux"
)

// A compute cell that writes an ARRAY (buffer) field seeds a valid no-op macro-WAT cell
// via array-element macros — (setidx pos (i32.const 0) (atidx pos (i32.const 0))) —
// instead of failing (which fell back to raw WAT). Scalar ports write back with
// (set field (get field)). The seed must expand + assemble.
func TestMacroNoopSeedsBufferPort(t *testing.T) {
	layout := flux.Layout{
		"count": {Type: flux.TInt, Offset: 0xB0000},
		"pos":   {Type: flux.TBuffer, Offset: 0xB0100, Len: 64},
	}
	arrays := map[string]bool{"pos": true}
	sub := Subsystem{Identity: "urn:hdm:apps:x:sim", Kind: KindCompute, Writes: []string{"count", "pos"}}
	src, ok := macroNoop(sub, layout, arrays)
	if !ok {
		t.Fatal("a cell writing an addressable buffer must seed a macro no-op, not fall back")
	}
	if !strings.Contains(src, "(setidx pos (i32.const 0) (atidx pos (i32.const 0)))") {
		t.Errorf("buffer no-op element store missing:\n%s", src)
	}
	if !strings.Contains(src, "(set count (get count))") {
		t.Errorf("scalar no-op write missing:\n%s", src)
	}
	if _, err := flux.Expand(src, layout); err != nil {
		t.Fatalf("buffer no-op seed must expand: %v\n%s", err, src)
	}
}

package appgen

import (
	"strings"
	"testing"

	"github.com/mdcfrancis/flow/flux"
)

// A compute cell that writes an ARRAY (buffer) field seeds a valid no-op via a store
// terminal — (store pos 0 (at pos 0)) — instead of failing (which fell back to raw WAT).
// Scalar ports still write back with (field field). The seed must compile.
func TestNoopFluxSeedsBufferPort(t *testing.T) {
	layout := flux.Layout{
		"count": {Type: flux.TInt, Offset: 0xB0000},
		"pos":   {Type: flux.TBuffer, Offset: 0xB0100, Len: 64},
	}
	sub := Subsystem{Identity: "urn:hdm:apps:x:sim", Kind: KindCompute, Writes: []string{"count", "pos"}}
	src, ok := noopFlux(sub, layout)
	if !ok {
		t.Fatal("a cell writing an addressable buffer must seed a Flux no-op, not fall back")
	}
	if !strings.Contains(src, "(store pos 0 (at pos 0))") {
		t.Errorf("buffer no-op store missing:\n%s", src)
	}
	if !strings.Contains(src, "(count count)") {
		t.Errorf("scalar no-op write missing:\n%s", src)
	}
	if _, err := flux.Compile("cell", src, layout); err != nil {
		t.Fatalf("buffer no-op seed must compile: %v\n%s", err, src)
	}
}

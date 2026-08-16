package evolution

import (
	"math"
	"testing"

	"github.com/mdcfrancis/flow/flux"
)

// An f32[N] contract field must map to an addressable float BUFFER — Type TBuffer with
// EType TFloat — so a particle system's positions live in the contract as native floats
// (not dropped as "not addressable", which is what forced physics onto flooring i32).
func TestLayoutFromContractFloatArray(t *testing.T) {
	c := &AppContract{Fields: []ContractField{
		{Name: "n", Offset: 0xB0000, Type: "i32"},
		{Name: "particle_x", Offset: 0xB0100, Type: "f32[256]"},
		{Name: "grid", Offset: 0xC0000, Type: "i32[64]"},
	}}
	l := LayoutFromContract(c)
	if l == nil {
		t.Fatal("layout is nil")
	}
	px, ok := l["particle_x"]
	if !ok {
		t.Fatal("f32[256] field was dropped — not addressable")
	}
	if px.Type != flux.TBuffer || px.EType != flux.TFloat || px.Len != 256 {
		t.Fatalf("f32[256] mapped wrong: %+v", px)
	}
	if g := l["grid"]; g.Type != flux.TBuffer || g.EType != flux.TInt {
		t.Fatalf("i32[64] must stay an i32 buffer: %+v", g)
	}
}

// An f32 scalar's Init is a decimal count; InitSeeds must seed its FLOAT bit pattern so a
// cell reading it with f32.load sees the real value, not a denormal from the raw integer.
func TestInitSeedsFloatScalar(t *testing.T) {
	c := &AppContract{Fields: []ContractField{
		{Name: "attractor_x", Offset: 0xB0000, Type: "f32", Init: 160},
		{Name: "score", Offset: 0xB0004, Type: "i32", Init: 7},
	}}
	seeds := c.InitSeeds()
	byOff := map[string]uint32{}
	for _, s := range seeds {
		byOff[s.At] = s.U32[0]
	}
	if got, want := byOff["0xB0000"], math.Float32bits(160); got != want {
		t.Fatalf("f32 init: got 0x%X want 0x%X (float bits of 160.0)", got, want)
	}
	if byOff["0xB4"] != 0 && byOff["0xB0004"] != 7 {
		t.Fatalf("i32 init should be the integer 7, got %v", byOff)
	}
}

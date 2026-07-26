package evolution

import (
	"testing"

	"github.com/mdcfrancis/flow/flux"
)

// Local singleton binding: a required scalar capability is allocated the next
// slider register as a read-only field under its alias, so the cell lowers to a
// read from the bound address without ever naming a register. The base layout is
// never mutated.
func TestBindCapabilitiesAssignsSliderRegister(t *testing.T) {
	src := `(cell speed-adapter (requires (scalar speed 0 255)) (writes ball_speed)
	          (write (ball_speed speed)))`
	base := flux.Layout{"ball_speed": {Type: flux.TInt, Offset: 0xB0010}}

	aug := BindCapabilities(base, src)
	f, ok := aug["speed"]
	if !ok {
		t.Fatal("scalar capability `speed` was not bound")
	}
	if !f.ReadOnly || f.Offset != 0x50024 { // == execution.InSlider0
		t.Fatalf("speed bound to %+v, want ReadOnly @ 0x50024 (hmi_slider0)", f)
	}
	if _, mutated := base["speed"]; mutated {
		t.Error("BindCapabilities mutated the caller's base layout")
	}

	// The whole cell lowers through the bound layout (lowerFluxToBytecode binds).
	if _, err := lowerFluxToBytecode(base, src); err != nil {
		t.Fatalf("capability cell did not lower: %v", err)
	}
}

// Two scalar capabilities take successive slider registers.
func TestBindCapabilitiesAllocatesSuccessively(t *testing.T) {
	src := `(cell twoknobs (requires (scalar a 0 10) (scalar b 0 10)) (writes ball_x ball_y)
	          (write (ball_x a) (ball_y b)))`
	base := flux.Layout{
		"ball_x": {Type: flux.TInt, Offset: 0xB0000},
		"ball_y": {Type: flux.TInt, Offset: 0xB0004},
	}
	aug := BindCapabilities(base, src)
	if aug["a"].Offset != 0x50024 || aug["b"].Offset != 0x50028 {
		t.Fatalf("successive scalars = a@0x%X b@0x%X, want 0x50024/0x50028", aug["a"].Offset, aug["b"].Offset)
	}
}

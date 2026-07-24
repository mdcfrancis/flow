package evolution

import (
	"fmt"
	"testing"

	"github.com/mdcfrancis/flow/execution"
	"github.com/mdcfrancis/flow/flux"
)

// The read-only hmi_slider capability fields are hand-mirrored from the runtime's
// InSlider0/NumSliders (flux_bridge.go duplicates the offsets to avoid an import),
// so guard the mirror against drift: every slider must exist at the matching
// offset, be read-only, and be an Int — and there must be no stray extra slider.
func TestHMISliderOffsetsMatchRuntime(t *testing.T) {
	for i := 0; i < execution.NumSliders; i++ {
		name := fmt.Sprintf("hmi_slider%d", i)
		f, ok := hmiFields[name]
		if !ok {
			t.Fatalf("%s missing from hmiFields", name)
		}
		if want := uint32(execution.InSlider0 + i*4); f.Offset != want {
			t.Errorf("%s offset = 0x%X, want 0x%X (execution.InSlider0 + %d*4)", name, f.Offset, want, i)
		}
		if !f.ReadOnly {
			t.Errorf("%s must be ReadOnly — the operator owns the value, a cell may only read it", name)
		}
		if f.Type != flux.TInt {
			t.Errorf("%s must be TInt, got %v", name, f.Type)
		}
	}
	if _, ok := hmiFields[fmt.Sprintf("hmi_slider%d", execution.NumSliders)]; ok {
		t.Errorf("hmi_slider%d exists but execution.NumSliders=%d", execution.NumSliders, execution.NumSliders)
	}
}

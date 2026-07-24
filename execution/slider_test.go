package execution

import "testing"

// A slider write lands in its own register, independent of the others, and the
// whole bank stays inside the HMI input region.
func TestSetSliderWritesRegister(t *testing.T) {
	rm, _ := newRM(t)
	if err := rm.SetSlider(0, 42); err != nil {
		t.Fatalf("slider 0: %v", err)
	}
	if err := rm.SetSlider(3, 200); err != nil {
		t.Fatalf("slider 3: %v", err)
	}
	if got := int32(rm.readU32(InSlider0)); got != 42 {
		t.Fatalf("slider0 = %d, want 42", got)
	}
	if got := int32(rm.readU32(InSlider0 + 3*4)); got != 200 {
		t.Fatalf("slider3 = %d, want 200", got)
	}
	// Setting slider 3 must not have disturbed slider 0.
	if got := int32(rm.readU32(InSlider0)); got != 42 {
		t.Fatalf("slider0 clobbered by slider3 write: %d", got)
	}
	// The whole bank must fit below InputEnd.
	if InSlider0+(NumSliders-1)*4 >= InputEnd {
		t.Fatalf("slider bank overflows the input region (last=0x%X, end=0x%X)", InSlider0+(NumSliders-1)*4, InputEnd)
	}
}

// An out-of-range slider index is rejected and writes nothing.
func TestSetSliderRejectsOutOfRange(t *testing.T) {
	rm, _ := newRM(t)
	for _, idx := range []int{-1, NumSliders, NumSliders + 5} {
		if err := rm.SetSlider(idx, 123); err == nil {
			t.Fatalf("index %d should be rejected", idx)
		}
	}
	if got := rm.readU32(InSlider0); got != 0 {
		t.Fatalf("a rejected slider write must not touch the register, got %d", got)
	}
}

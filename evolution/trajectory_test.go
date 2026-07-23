package evolution

import "testing"

func u32seq(vs ...uint32) []uint32 { return vs }

func TestMatchTrajectoryMinDistinct(t *testing.T) {
	tj := TrajectoryExpect{At: "0xB0000", MinDistinct: 4}
	// freeze: one value -> fail
	if matchTrajectory(tj, u32seq(5, 5, 5, 5, 5)) {
		t.Error("a frozen field (1 distinct) must fail MinDistinct=4")
	}
	// jitter between two -> fail
	if matchTrajectory(tj, u32seq(0, 600, 0, 600, 0, 600)) {
		t.Error("jitter between 2 values must fail MinDistinct=4")
	}
	// real traversal -> pass
	if !matchTrajectory(tj, u32seq(100, 105, 110, 115, 120, 125)) {
		t.Error("genuine traversal (many distinct) must pass MinDistinct=4")
	}
	// empty -> fail
	if matchTrajectory(tj, nil) {
		t.Error("empty trajectory must fail")
	}
}

func TestMatchTrajectoryInBounds(t *testing.T) {
	lo, hi := int32(0), int32(300)
	tj := TrajectoryExpect{At: "0xB0000", InBoundsMin: &lo, InBoundsMax: &hi}
	if !matchTrajectory(tj, u32seq(0, 150, 300, 150)) {
		t.Error("all in-bounds should pass")
	}
	if matchTrajectory(tj, u32seq(0, 150, 600)) { // 600 > 300
		t.Error("a value past the max must fail in-bounds")
	}
	// negative (as signed) below min
	var m5 int32 = -5
	if matchTrajectory(tj, u32seq(0, uint32(m5))) {
		t.Error("a negative value below min must fail in-bounds")
	}
}

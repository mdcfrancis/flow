package evolution

import "testing"

func TestFieldRange(t *testing.T) {
	c := &AppContract{Fields: []ContractField{
		{Name: "player_x", Offset: 0xB0000, Type: "i32"},
		{Name: "grid", Offset: 0xB0100, Type: "i32[64]"},
	}}
	if off, n, ok := c.FieldRange("PLAYER_X"); !ok || off != 0xB0000 || n != 4 {
		t.Fatalf("player_x: off=0x%X n=%d ok=%v", off, n, ok)
	}
	if off, n, ok := c.FieldRange("grid"); !ok || off != 0xB0100 || n != 256 {
		t.Fatalf("grid: off=0x%X n=%d ok=%v want n=256", off, n, ok)
	}
	if _, _, ok := c.FieldRange("nope"); ok {
		t.Fatal("nope should not resolve")
	}
}

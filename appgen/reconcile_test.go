package appgen

import (
	"testing"

	"github.com/mdcfrancis/flow/evolution"
)

// Every field a component declares reading/writing must end up in the contract,
// so its port always resolves to an offset. "HMI input" is the fixed input
// register, not a contract field, and is skipped.
func TestReconcileDeclaredFieldsAddsMissing(t *testing.T) {
	env := &AppEnvelope{SubsystemRequirements: []Subsystem{
		{Identity: "a", Reads: []string{"HMI input"}, Writes: []string{"player_x"}},
		{Identity: "b", Reads: []string{"player_x", "bullet_x"}, Writes: []string{"bullet_x", "bullet_active"}},
	}}
	// Contract as authored covers only player_x; the rest must be added.
	fields := []evolution.ContractField{{Name: "player_x", Offset: 0xB0000, Type: "i32"}}
	out := reconcileDeclaredFields(fields, env)

	have := map[string]int{}
	for _, f := range out {
		have[f.Name] = f.Offset
	}
	for _, want := range []string{"player_x", "bullet_x", "bullet_active"} {
		if _, ok := have[want]; !ok {
			t.Errorf("contract missing declared field %q: %v", want, have)
		}
	}
	if _, ok := have["HMI input"]; ok {
		t.Error("HMI input must NOT be added as a contract field")
	}
	// Added fields get distinct, in-region offsets.
	seen := map[int]bool{}
	for _, f := range out {
		if f.Offset < 0xB0000 || f.Offset >= 0xC0000 {
			t.Errorf("field %s at 0x%X is outside the sandbox region", f.Name, f.Offset)
		}
		if seen[f.Offset] {
			t.Errorf("field %s reuses offset 0x%X", f.Name, f.Offset)
		}
		seen[f.Offset] = true
	}
}

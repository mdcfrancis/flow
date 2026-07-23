package evolution

import "strings"

import "testing"

// TestStructuralFlag: the orchestrator's structural flag toggles per cell.
func TestStructuralFlag(t *testing.T) {
	o := &Orchestrator{}
	if o.IsStructural("urn:x") {
		t.Fatal("fresh cell must not be structural")
	}
	o.SetStructural("urn:x", true)
	if !o.IsStructural("urn:x") {
		t.Fatal("SetStructural(true) must flag the cell")
	}
	o.SetStructural("urn:y", true)
	o.SetStructural("urn:x", false)
	if o.IsStructural("urn:x") {
		t.Fatal("SetStructural(false) must clear the cell")
	}
	if !o.IsStructural("urn:y") {
		t.Fatal("clearing one cell must not affect another")
	}
}

// TestStructureToolkitContent: the toolkit offers the real primitive ABIs, the paging/dispatch
// imports, and the NEW-PRIMITIVE minting protocol — so the model can both reuse and create.
func TestStructureToolkitContent(t *testing.T) {
	tk := renderStructural()
	for _, must := range []string{
		"sys:dict", "sys:set", "sys:list", // reuse vocabulary
		"hdm:kernel/paging", "invoke-cell", // the ABI to reach a primitive
		"NEW-PRIMITIVE",                     // the minting protocol
		"replayed against regression tapes", // the correctness constraint
	} {
		if !strings.Contains(tk, must) {
			t.Errorf("structure toolkit missing %q", must)
		}
	}
}

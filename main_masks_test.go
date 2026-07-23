package main

import (
	"testing"

	"github.com/mdcfrancis/flow/evolution"
)

// TestBuildMasksDenyByDefault: a cell may touch ONLY the fields it explicitly declared;
// every other contract field becomes a poison (read) / revert (write) range.
func TestBuildMasksDenyByDefault(t *testing.T) {
	ct := &evolution.AppContract{Fields: []evolution.ContractField{
		{Name: "a", Offset: 0xB0000, Type: "i32"},
		{Name: "b", Offset: 0xB0004, Type: "i32"},
		{Name: "c", Offset: 0xB0008, Type: "i32"},
	}}
	comp := &evolution.ComponentMap{
		Identity:       "urn:hdm:apps:x:cell",
		DeclaredReads:  []string{"a"}, // may read a only
		DeclaredWrites: []string{"b"}, // may write b only
		// note: verified Reads/Writes are IGNORED — only explicit declarations grant access
		Reads:  []string{"c"},
		Writes: []string{"c"},
	}
	poison, revert := buildMasks(comp, ct)
	has := func(rs [][2]uint32, off uint32) bool {
		for _, r := range rs {
			if r[0] == off {
				return true
			}
		}
		return false
	}
	// poison = fields the cell can neither read nor write = c only (a is readable, b is
	// writable so it is never poisoned — that would risk leaking a 0).
	if has(poison, 0xB0000) || has(poison, 0xB0004) || !has(poison, 0xB0008) {
		t.Errorf("poison wrong: %v (want c only)", poison)
	}
	// revert = fields NOT declared-writable = a, c (not b)
	if !has(revert, 0xB0000) || has(revert, 0xB0004) || !has(revert, 0xB0008) {
		t.Errorf("revert wrong: %v (want a,c not b)", revert)
	}
}

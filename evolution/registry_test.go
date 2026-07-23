package evolution

import "testing"

func TestCellRegistry(t *testing.T) {
	r := NewCellRegistry("a", "b")
	r.Add("b", "c", "", "a") // dedup + ignore blank
	got := r.List()
	if len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Fatalf("registry = %v, want [a b c]", got)
	}
	// List returns a copy — mutating it must not affect the registry.
	got[0] = "x"
	if r.List()[0] != "a" {
		t.Fatal("List did not return a defensive copy")
	}

	// Remove drops URNs (order-preserving) and clears them from the seen set so
	// they can be re-Added (a fractured parent could conceivably return).
	r.Remove("b", "z") // "z" unknown → ignored
	if got := r.List(); len(got) != 2 || got[0] != "a" || got[1] != "c" {
		t.Fatalf("after Remove = %v, want [a c]", got)
	}
	r.Add("b")
	if got := r.List(); len(got) != 3 || got[2] != "b" {
		t.Fatalf("re-Add after Remove = %v, want [a c b]", got)
	}
}

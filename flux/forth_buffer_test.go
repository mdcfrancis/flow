package flux

import (
	"strings"
	"testing"
)

// The Forth surface handles the buffer primitives: `buf idx at` reads an element and
// `val buf idx store` is a write terminal. A buffer cell round-trips s-expr → Forth →
// identical WAT, so array-writing cells (particle systems, grid renderers) are Forth.
func TestForthBufferAtStore(t *testing.T) {
	layout := Layout{
		"g": {Type: TBuffer, Offset: 0xB0100, Len: 64},
		"n": {Type: TInt, Offset: 0xB0000},
	}
	cases := []string{
		"(cell c (reads g) (writes g) (write (store g 0 (at g 0))))",             // no-op seed
		"(cell c (reads g n) (writes g) (write (store g n (+ (at g 0) 1))))",     // read+compute+store
		"(cell c (reads g n) (writes g) (write (store g n (at g n))))",           // element copy
	}
	for _, src := range cases {
		cell, err := (SExpr{}).Read("s", src, layout)
		if err != nil {
			t.Fatalf("read %q: %v", src, err)
		}
		forth := (Forth{}).Render(cell)
		if forth == "" || strings.Contains(forth, "(cell") {
			t.Fatalf("Forth.Render should emit a word stream, got %q for %q", forth, src)
		}
		wS, e1 := Compile("c", src, layout)
		wF, e2 := CompileWith(Forth{}, "c", forth, layout)
		if e1 != nil || e2 != nil {
			t.Fatalf("compile s=%v f=%v (forth=%q)", e1, e2, forth)
		}
		if wS != wF {
			t.Errorf("Forth buffer cell != s-expr WAT\n src=%q\n forth=%q", src, forth)
		}
	}
}

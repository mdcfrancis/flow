package flux

import (
	"strings"
	"testing"
)

// The typed Forth GBNF must include buffer productions when the layout has array
// fields, and the forms it describes must actually parse + compile.
func TestGBNFForthTypedBuffers(t *testing.T) {
	layout := Layout{
		"n": {Type: TInt, Offset: 0xB0000},
		"g": {Type: TBuffer, Offset: 0xB0100, Len: 64},
	}
	g := GBNFForthTyped(layout, KindCompute)
	if g == "" {
		t.Fatal("grammar should be non-empty for a buffer-writing compute cell")
	}
	for _, want := range []string{`buffield ::= "g"`, `" at"`, "bstore ::=", `" store"`, "bwrite ::=", "stmt ::="} {
		if !strings.Contains(g, want) {
			t.Errorf("grammar missing %q\n%s", want, g)
		}
	}
	// Forms the grammar admits must round-trip to WAT.
	for _, src := range []string{
		"g 0 at 1 + g n store",       // read el 0, +1, store to el n
		"g n at g 0 store",           // copy el n -> el 0
		"n g 0 at + -> n",            // buffer read feeds a scalar write
	} {
		if _, err := CompileWith(Forth{}, "c", src, layout); err != nil {
			t.Errorf("grammar-shaped forth %q must compile: %v", src, err)
		}
	}
}

// A buffer-only compute cell (no scalar int fields) still gets a grammar.
func TestGBNFForthTypedBufferOnly(t *testing.T) {
	layout := Layout{"g": {Type: TBuffer, Offset: 0xB0100, Len: 64}}
	if GBNFForthTyped(layout, KindCompute) == "" {
		t.Fatal("a buffer-only compute cell must still get a grammar")
	}
}

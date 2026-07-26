package flux

import (
	"strings"
	"testing"
)

var bufLayout = Layout{
	"tokens": {Type: TBuffer, Offset: 0xB1000, Len: 8},
	"cursor": {Type: TInt, Offset: 0xB0000, ReadOnly: true},
	"out":    {Type: TInt, Offset: 0xB0004},
}

// A Flux cell can now index a buffer: (at tokens cursor) reads tokens[cursor], and
// (len tokens) is its size. It compiles to a bounds-CLAMPED i32 load at the buffer's
// base — the capability parser/stream cells need.
func TestBufferAtLowers(t *testing.T) {
	src := `(cell peek (reads tokens cursor) (writes out) (write (out (at tokens cursor))))`
	wat, err := Compile("m", src, bufLayout)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	// The load is at the buffer base (0xB1000), indexed by a clamped, *4-strided offset,
	// and tokens is NOT loaded as a scalar local.
	if !strings.Contains(wat, "i32.load (i32.add (i32.const 0xB1000)") {
		t.Fatalf("expected an indexed load at the buffer base:\n%s", wat)
	}
	if !strings.Contains(wat, "i32.mul") || !strings.Contains(wat, "select") {
		t.Fatalf("expected a *4 stride and a bounds-clamp select:\n%s", wat)
	}
	if strings.Contains(wat, "(local $tokens") {
		t.Fatalf("a buffer must not be a scalar local:\n%s", wat)
	}
}

func TestBufferLen(t *testing.T) {
	src := `(cell sz (reads tokens) (writes out) (write (out (len tokens))))`
	wat, err := Compile("m", src, bufLayout)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if !strings.Contains(wat, "(i32.store (i32.const 0xB0004) (i32.const 8))") {
		t.Fatalf("(len tokens) should be the constant 8:\n%s", wat)
	}
}

func TestBufferStoreLowers(t *testing.T) {
	l := Layout{
		"tokens": {Type: TBuffer, Offset: 0xB1000, Len: 8},
		"cursor": {Type: TInt, Offset: 0xB0000}, // writable here
	}
	src := `(cell w (reads tokens cursor) (writes tokens cursor)
	          (write (store tokens cursor (+ (at tokens cursor) 1)) (cursor (+ cursor 1))))`
	wat, err := Compile("m", src, l)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	// a clamped store into the buffer, plus the scalar cursor advance.
	if !strings.Contains(wat, "i32.store (i32.add (i32.const 0xB1000)") {
		t.Fatalf("expected a clamped store at the buffer base:\n%s", wat)
	}
	if !strings.Contains(wat, "(i32.store (i32.const 0xB0000)") {
		t.Fatalf("expected the scalar cursor advance:\n%s", wat)
	}
}

func TestBufferStoreTypeErrors(t *testing.T) {
	// storing into a read-only buffer is rejected.
	ro := Layout{"tokens": {Type: TBuffer, Offset: 0xB1000, Len: 8, ReadOnly: true}, "out": {Type: TInt, Offset: 0xB0000}}
	if _, err := Compile("m", `(cell c (reads tokens) (writes out) (write (out 0) (store tokens 0 5)))`, ro); err == nil {
		t.Fatal("store into a read-only buffer must be a type error")
	}
	// storing a non-Int value is rejected.
	if _, err := Compile("m", `(cell c (reads tokens) (writes tokens) (write (store tokens 0 (>= 1 0))))`, bufLayout); err == nil {
		t.Fatal("storing a Bool must be a type error")
	}
}

func TestBufferTypeErrors(t *testing.T) {
	// at on a scalar is a type error.
	if _, err := Compile("m", `(cell c (reads cursor) (writes out) (write (out (at cursor cursor))))`, bufLayout); err == nil {
		t.Fatal("at on a non-buffer must be a type error")
	}
	// a buffer used as a plain Int is a type error (it is not an i32 value).
	if _, err := Compile("m", `(cell c (reads tokens) (writes out) (write (out (+ tokens 1))))`, bufLayout); err == nil {
		t.Fatal("using a Buffer as an Int must be a type error")
	}
}

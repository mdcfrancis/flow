package flux

import (
	"strings"
	"testing"
)

// A scalar capability: the cell names the capability (scalar speed 0 255), the
// system has bound `speed` to a read-only resource offset, and the cell reads it
// like any field — lowering to a load from the bound address, never naming it.
func TestScalarCapabilityCompiles(t *testing.T) {
	// The system's binding: `speed` -> a read-only slider register at 0x50024.
	layout := Layout{
		"ball_speed": {Type: TInt, Offset: 0xB0010},
		"speed":      {Type: TInt, Offset: 0x50024, ReadOnly: true},
	}
	src := `(cell speed-adapter (requires (scalar speed 0 255)) (writes ball_speed)
	          (write (ball_speed speed)))`

	// Requirements extracts the capability without a layout (the binder uses this).
	reqs, err := Requirements(src)
	if err != nil || len(reqs) != 1 {
		t.Fatalf("Requirements = %v, %v; want 1 scalar", reqs, err)
	}
	if reqs[0].Kind != "scalar" || reqs[0].Alias != "speed" || reqs[0].Min != 0 || reqs[0].Max != 255 {
		t.Fatalf("parsed capability = %+v", reqs[0])
	}

	wat, err := Compile("cell", src, layout)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	// The bound capability lowers to a load from its resource offset...
	if !strings.Contains(wat, "i32.load (i32.const 0x50024)") {
		t.Fatalf("capability did not lower to a load from 0x50024:\n%s", wat)
	}
	// ...and it exports run-tick (a compute cell that forwards the input to state).
	if !strings.Contains(wat, `(export "run-tick")`) {
		t.Fatalf("expected run-tick export:\n%s", wat)
	}
}

// A capability is read-only: writing its alias is a type error.
func TestScalarCapabilityIsReadOnly(t *testing.T) {
	layout := Layout{
		"speed": {Type: TInt, Offset: 0x50024, ReadOnly: true},
	}
	_, err := Compile("bad", `(cell x (requires (scalar speed 0 255)) (writes speed)
	          (write (speed 0)))`, layout)
	if err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("writing a capability must be a read-only error, got: %v", err)
	}
}

// An unbound capability (the system failed to allocate its resource) is an error,
// not a silent read of zero.
func TestScalarCapabilityMustBeBound(t *testing.T) {
	layout := Layout{"ball_speed": {Type: TInt, Offset: 0xB0010}}
	_, err := Compile("bad", `(cell x (requires (scalar speed 0 255)) (writes ball_speed)
	          (write (ball_speed speed)))`, layout)
	if err == nil || !strings.Contains(err.Error(), "not bound") {
		t.Fatalf("an unbound capability must error, got: %v", err)
	}
}

// A capability-requiring cell satisfies the input-source interface (it reads a
// read-only resource), so downstream capability implications apply.
func TestCapabilityCellIsInputSource(t *testing.T) {
	layout := Layout{
		"ball_speed": {Type: TInt, Offset: 0xB0010},
		"speed":      {Type: TInt, Offset: 0x50024, ReadOnly: true},
	}
	f, _ := Parse("c", `(cell a (requires (scalar speed 0 255)) (writes ball_speed) (write (ball_speed speed)))`)
	cell, err := Check(f, layout)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if !NeedsHMIInput(cell, layout) {
		t.Fatal("a capability-requiring cell must be an input-source")
	}
}

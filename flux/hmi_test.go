package flux

import (
	"strings"
	"testing"
)

// A read-only capability field (like an HMI input register) may be READ but a
// write to it is a type error — the host owns it.
func TestReadOnlyFieldCannotBeWritten(t *testing.T) {
	layout := Layout{
		"player_x": {Type: TInt, Offset: 0xB0000},
		"hmi_key":  {Type: TInt, Offset: 0x50020, ReadOnly: true},
	}
	// Reading the input to steer state is fine.
	if _, err := Compile("ok", `
		(cell input (reads hmi_key player_x) (writes player_x)
		  (write (player_x (if (= hmi_key 68) (+ player_x 4) player_x))))`, layout); err != nil {
		t.Fatalf("reading a read-only input should compile: %v", err)
	}
	// Writing the input register is rejected.
	_, err := Compile("bad", `(cell input (reads hmi_key) (writes hmi_key)
		  (write (hmi_key 0)))`, layout)
	if err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("writing a read-only field must be a type error, got: %v", err)
	}
}

// An HMI-reading cell lowers to a load from the register's absolute offset.
func TestHMIFieldLowersToRegisterLoad(t *testing.T) {
	layout := Layout{
		"player_x": {Type: TInt, Offset: 0xB0000},
		"hmi_key":  {Type: TInt, Offset: 0x50020, ReadOnly: true},
	}
	wat, err := Compile("input", `
		(cell input (reads hmi_key player_x) (writes player_x)
		  (write (player_x (if (= hmi_key 39) (+ player_x 4) player_x))))`, layout)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if !strings.Contains(wat, "i32.load (i32.const 0x50020)") {
		t.Fatalf("hmi_key did not lower to a load from 0x50020:\n%s", wat)
	}
}

package evolution

// A shared-state CONTRACT lets the subsystems of one application coordinate
// despite being evolved in isolation. It names the shared-memory fields (in the
// persistent sandbox region) that cells read/write to talk to each other — e.g.
// the input handler writes player_x, the renderer reads it. The contract is
// authored once per app, persisted, and injected into every subsystem's build
// seed and its scenario authoring, so all cells agree on the same layout.

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mdcfrancis/flow/storage"
)

// ContractField is one named shared-memory slot.
type ContractField struct {
	Name   string `json:"name"`
	Offset int    `json:"offset"` // absolute shared-memory offset (sandbox region)
	Type   string `json:"type"`   // e.g. "i32", "i32[40]"
	Desc   string `json:"desc"`
	// Init is the field's initial value — the MOCK/boot state authored during the
	// specification phase. It is what every cell is tested against (a scenario
	// baseline) and what the live app boots from, so a cell that reads a config
	// field (e.g. screen_width) or a sibling-produced field sees a real value
	// instead of zero. A test case overrides it for the specific field it exercises.
	Init int `json:"init,omitempty"`
}

// InitSeeds renders the contract's initialization as a baseline set of seeds — one
// per scalar field at its Init value. Prepended to a scenario's own seeds it mocks
// the whole world the cell runs in; overlaid on live boot it starts the app in a
// valid, moving state. Array fields are skipped (no scalar init).
func (c *AppContract) InitSeeds() []SeedWrite {
	if c == nil {
		return nil
	}
	var out []SeedWrite
	for _, f := range c.Fields {
		if typeWords(f.Type) != 1 { // scalars only
			continue
		}
		out = append(out, SeedWrite{At: fmt.Sprintf("0x%X", f.Offset), U32: []uint32{uint32(int32(f.Init))}})
	}
	return out
}

// AppContract is the shared-state layout every subsystem of an app agrees on.
type AppContract struct {
	Fields []ContractField `json:"fields"`
}

// FieldRange returns a field's absolute offset and byte length by name
// (case-insensitive), or ok=false. Used to hash a cell's declared read-set for
// frame-level memoization (skip a cell whose inputs are unchanged).
func (c *AppContract) FieldRange(name string) (offset, byteLen int, ok bool) {
	if c == nil {
		return 0, 0, false
	}
	for _, f := range c.Fields {
		if strings.EqualFold(f.Name, name) {
			return f.Offset, typeWords(f.Type) * 4, true
		}
	}
	return 0, 0, false
}

func contractRefURN(namespace string) string { return namespace + ":contract" }

// SaveContract persists an app's shared-state contract.
func SaveContract(ledger *storage.LedgerEngine, namespace string, c *AppContract) error {
	raw, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("encode contract: %w", err)
	}
	h, err := ledger.WriteBlock(raw)
	if err != nil {
		return fmt.Errorf("persist contract: %w", err)
	}
	return ledger.UpdateRef(contractRefURN(namespace), h)
}

// LoadContract returns an app's shared-state contract, or nil if none exists.
func LoadContract(ledger *storage.LedgerEngine, namespace string) *AppContract {
	h, err := ledger.GetRef(contractRefURN(namespace))
	if err != nil {
		return nil
	}
	raw, err := ledger.ReadBlock(h)
	if err != nil {
		return nil
	}
	var c AppContract
	if json.Unmarshal(raw, &c) != nil || len(c.Fields) == 0 {
		return nil
	}
	return &c
}

// Render formats the contract for a synthesis/authoring prompt, or "" if empty.
func (c *AppContract) Render() string {
	if c == nil || len(c.Fields) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("SHARED-STATE CONTRACT — every subsystem of this application coordinates through\n")
	b.WriteString("these EXACT shared-memory fields (read/write them to interoperate with sibling\n")
	b.WriteString("cells; keep private scratch elsewhere):\n")
	for _, f := range c.Fields {
		fmt.Fprintf(&b, "- %s: %s @ 0x%X — %s\n", f.Name, f.Type, f.Offset, f.Desc)
	}
	return b.String()
}

// AppNamespaceOf extracts urn:hdm:apps:<name> from a subsystem cell URN, or ""
// if the URN is not an application subsystem.
func AppNamespaceOf(urn string) string {
	const prefix = "urn:hdm:apps:"
	if !strings.HasPrefix(urn, prefix) {
		return ""
	}
	rest := urn[len(prefix):]
	if i := strings.IndexByte(rest, ':'); i >= 0 {
		rest = rest[:i]
	}
	if rest == "" {
		return ""
	}
	return prefix + rest
}

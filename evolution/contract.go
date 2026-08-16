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
	"math"
	"strings"

	"github.com/mdcfrancis/flow/storage"
)

// Contract ARENAS. Contract offsets are absolute addresses into the one shared
// linear memory, so every application needs its own window or two live apps
// overwrite each other field-for-field. The geometry is:
//
//	0xB0000..0xC0000  host-return scratch (the hypervisor's bump allocator) AND
//	                  the legacy arena — apps grown before arenas existed have
//	                  their offsets baked in here and keep them.
//	0xC0000..0x400000 52 per-app arenas of 64 KiB each.
//
// LegacyArenaBase is deliberately the old hardcoded base: an envelope with no
// assigned arena resolves to it, so an app already in the ledger keeps working at
// the offsets its cells were synthesized against. Only newly grown apps get a
// private arena.
//
// The upper bound must stay in step with the hypervisor's scratch ceiling
// (execution/hypervisor.go: scratchEnd) and its page window (windowBase); the two
// packages do not import each other, so the constants are cross-referenced by
// comment rather than shared.
const (
	ArenaSize       = 0x10000  // 64 KiB per application
	LegacyArenaBase = 0xB0000  // pre-arena apps (and the host-return scratch zone)
	FirstArenaBase  = 0xC0000  // first private arena, immediately above scratch
	ArenaLimit      = 0x400000 // == windowBase; arenas must stay below the page window
)

// MaxArenas is how many applications can hold a private contract arena at once.
const MaxArenas = (ArenaLimit - FirstArenaBase) / ArenaSize

// ArenaBaseAt returns the base address of the index'th private arena.
func ArenaBaseAt(index int) int { return FirstArenaBase + index*ArenaSize }

// ArenaEnd returns the exclusive upper bound of the arena starting at base.
func ArenaEnd(base int) int { return base + ArenaSize }

// InArena reports whether an absolute offset falls inside the arena at base.
func InArena(base, offset int) bool { return offset >= base && offset < ArenaEnd(base) }

// ContractField is one named shared-memory slot.
type ContractField struct {
	Name   string `json:"name"`
	Offset int    `json:"offset"` // absolute shared-memory offset (sandbox region)
	Type   string `json:"type"`   // e.g. "i32", "i32[40]"
	Desc   string `json:"desc"`
	// Stride is the byte gap between consecutive elements of an array field, in BYTES.
	// Zero means a dense array (stride = element width, 4). A non-zero stride marks this
	// field as one column of an INTERLEAVED collection buffer: particle_x, particle_y, …
	// occupy one array-of-structs block, each starting at its own Offset and stepping by
	// Stride (bytes-per-record). This is how a model's collection entity projects to one
	// buffer the map iterates by stride while cells still address fields by name.
	Stride int `json:"stride,omitempty"`
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
		// An f32 field's Init is a decimal count (e.g. an attractor at x=160): seed the
		// FLOAT bit pattern, not the raw integer, so a cell reading it with f32.load sees
		// 160.0 rather than a denormal. i32 fields seed the integer directly.
		bits := uint32(int32(f.Init))
		if strings.TrimSpace(f.Type) == "f32" {
			bits = math.Float32bits(float32(f.Init))
		}
		out = append(out, SeedWrite{At: fmt.Sprintf("0x%X", f.Offset), U32: []uint32{bits}})
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
			// A strided (interleaved) field's elements span the whole record block, so its
			// change-detection range is elementCount*stride bytes — conservative (it covers
			// the block), never wrongly skipping a cell whose element changed.
			step := 4
			if f.Stride > 0 {
				step = f.Stride
			}
			return f.Offset, typeWords(f.Type) * step, true
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

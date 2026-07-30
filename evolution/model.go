package evolution

// The SYSTEM MODEL is the design's SPINE — the one cohesive answer to "what is being
// built": the entities (the nouns), the typed state each carries, and the dynamics that
// transform that state each tick. Until now the contract (data), the component ports, and
// the plan (design) were each authored INDEPENDENTLY, with no shared vocabulary — so the
// same quantity drifted across artifacts ("x" in a port, "particle_x" in the contract, a
// phantom "particle_states" minted from a mismatched port) and nothing could rationalize
// whether the design cohered, because there was no single source of truth to cohere TO.
//
// The model fixes that at the root: it is authored FIRST, and the contract is a
// deterministic PROJECTION of it (names and types derived by one rule), while the
// component ports RESOLVE to its fields. Drift becomes structurally impossible — there is
// exactly one name and one type per modeled quantity — and the design critic's job
// collapses into a single deliberate question: does every artifact conform to the model?
//
// Layering becomes: MODEL (what) → contract (data, projected) + ports (resolved) → plan
// (design) → code (implementation) → map (as-built).

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"github.com/mdcfrancis/flow/storage"
)

// EntityField is one typed piece of an entity's state. The type is an ELEMENT type
// (i32 or f32); the array-ness comes from the entity's cardinality, not the field.
type EntityField struct {
	Name string `json:"name"` // "vx"
	Type string `json:"type"` // "i32" | "f32"
	Desc string `json:"desc,omitempty"`
}

// Entity is a noun in the system with typed state and a number of instances. A
// singleton (Cardinality <= 1) projects to scalar fields; a multi-instance entity (a
// particle, cardinality 100) projects to arrays plus an "<entity>_count" scalar.
type Entity struct {
	Name        string        `json:"name"`        // "particle"
	Cardinality int           `json:"cardinality"` // instances; <=1 = singleton
	Fields      []EntityField `json:"fields"`
	Purpose     string        `json:"purpose,omitempty"`
}

// FieldInit is the DESIGNED starting state of one entity field — the initial condition the
// system boots from, not a per-tick rule. It is what a particle system needs and the old
// pipeline lacked: a way to say "scatter these 200 positions across the screen" rather than
// leaving the array at zero (all particles stacked on one point). The distribution is
// expanded deterministically per element at boot:
//   - "uniform": each element a pseudo-random value in [Min,Max] (a 2D scatter when x and y
//     are both uniform — they use independent per-field salts, not the same sequence);
//   - "spread":  each element evenly stepped Min→Max by index (a ramp/line);
//   - "const":   every element = Value;
//   - "zero"/"": every element 0.
type FieldInit struct {
	Entity string  `json:"entity"`
	Field  string  `json:"field"`
	Dist   string  `json:"dist"`
	Min    float64 `json:"min,omitempty"`
	Max    float64 `json:"max,omitempty"`
	Value  float64 `json:"value,omitempty"`
}

// SystemModel is the canonical model of an application: its entities, the per-tick dynamics
// that transform their state, and the INITIAL CONDITIONS it boots from. The contract and
// ports are derived from it; the initial conditions seed live memory at boot.
type SystemModel struct {
	Namespace string      `json:"namespace"`
	Objective string      `json:"objective"`
	Entities  []Entity    `json:"entities"`
	Dynamics  []string    `json:"dynamics,omitempty"` // per-tick transformations, in the model's vocabulary
	Init      []FieldInit `json:"init,omitempty"`     // designed initial conditions (the boot state)
}

func modelRefURN(namespace string) string { return namespace + ":model" }

// SavePlan/LoadPlan mirror. SaveModel persists an application's system model.
func SaveModel(ledger *storage.LedgerEngine, namespace string, m *SystemModel) error {
	raw, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("encode model: %w", err)
	}
	h, err := ledger.WriteBlock(raw)
	if err != nil {
		return fmt.Errorf("persist model: %w", err)
	}
	return ledger.UpdateRef(modelRefURN(namespace), h)
}

// LoadModel returns an application's system model, or nil if none exists.
func LoadModel(ledger *storage.LedgerEngine, namespace string) *SystemModel {
	h, err := ledger.GetRef(modelRefURN(namespace))
	if err != nil {
		return nil
	}
	raw, err := ledger.ReadBlock(h)
	if err != nil {
		return nil
	}
	var m SystemModel
	if json.Unmarshal(raw, &m) != nil || len(m.Entities) == 0 {
		return nil
	}
	return &m
}

// projElemType clamps an entity field's type to the two element types the substrate
// addresses — a continuous quantity is f32, everything else i32.
func projElemType(t string) string {
	if strings.TrimSpace(strings.ToLower(t)) == "f32" {
		return "f32"
	}
	return "i32"
}

// ProjectFields derives the shared-state contract fields from the model by ONE rule and
// assigns their absolute offsets in the sandbox region:
//   - a SINGLETON entity's fields become dense scalars "<entity>_<field>";
//   - a COLLECTION entity (cardinality N>1) becomes an "<entity>_count" scalar plus one
//     INTERLEAVED buffer: its fields "<entity>_<field>" share one array-of-structs block,
//     each starting at base+fieldIndex*4 and stepping by Stride = fieldCount*4 bytes.
// Interleaving is what lets the map iterate the collection by a single stride and hand the
// leaf a pointer to ONE whole record (its packed element), while cells still address the
// columns by name. Because names, types, and offsets all come from here and nowhere else,
// a field's identity is canonical: no drift, no phantom fields.
func (m *SystemModel) ProjectFields() []ContractField {
	if m == nil {
		return nil
	}
	const regionBase, regionEnd = 0xB0000, 0xC0000
	off := regionBase
	var out []ContractField
	for _, e := range m.Entities {
		name := strings.TrimSpace(e.Name)
		if name == "" {
			continue
		}
		card := e.Cardinality
		if card < 1 {
			card = 1
		}
		// Valid (named) fields only, so the record stride is accurate.
		var fields []EntityField
		for _, f := range e.Fields {
			if strings.TrimSpace(f.Name) != "" {
				fields = append(fields, f)
			}
		}
		if card > 1 {
			if off+4 > regionEnd {
				break
			}
			out = append(out, ContractField{Name: name + "_count", Type: "i32", Offset: off, Desc: "number of active " + name + " instances", Init: card})
			off += 4
			// A collection projects to one dense array COLUMN per field. Cells update the
			// collection IN PLACE with a plain loop — (for $i (get <entity>_count)
			// (setidx <entity>_vx $i …)) — so the layout is simple struct-of-arrays; there
			// is no map combinator to demand a contiguous per-record element.
			for _, f := range fields {
				if off+card*4 > regionEnd {
					break
				}
				out = append(out, ContractField{
					Name:   name + "_" + strings.TrimSpace(f.Name),
					Type:   fmt.Sprintf("%s[%d]", projElemType(f.Type), card),
					Offset: off,
					Desc:   f.Desc,
				})
				off += card * 4
			}
			continue
		}
		for _, f := range fields {
			if off+4 > regionEnd {
				break
			}
			out = append(out, ContractField{Name: name + "_" + strings.TrimSpace(f.Name), Type: projElemType(f.Type), Offset: off, Desc: f.Desc})
			off += 4
		}
	}
	return out
}

// RecordStrideWords returns the number of words per instance of a collection entity — the
// map's ElemWords when iterating that entity's interleaved buffer.
func (e *Entity) RecordStrideWords() int {
	n := 0
	for _, f := range e.Fields {
		if strings.TrimSpace(f.Name) != "" {
			n++
		}
	}
	return n
}

// InitialSeeds expands the model's designed initial conditions into the boot SeedWrites —
// one per initialized field, with an array field's every element filled from its
// distribution. Offsets/types come from the projected contract, so the seeds land exactly
// where the field lives (i32 fields as integers, f32 fields as float bit patterns). This is
// the DESIGNED cold start a particle system needs, replacing the old "arrays boot at zero".
func (m *SystemModel) InitialSeeds(c *AppContract) []SeedWrite {
	if m == nil || c == nil {
		return nil
	}
	byName := map[string]ContractField{}
	for _, f := range c.Fields {
		byName[strings.ToLower(f.Name)] = f
	}
	var out []SeedWrite
	for _, ic := range m.Init {
		name := strings.ToLower(strings.TrimSpace(ic.Entity) + "_" + strings.TrimSpace(ic.Field))
		f, ok := byName[name]
		if !ok {
			continue
		}
		if f.Stride > 0 {
			continue // interleaved columns aren't consecutive words — dense collections only (v1)
		}
		count := typeWords(f.Type)
		if count < 1 {
			count = 1
		}
		isF32 := strings.HasPrefix(strings.TrimSpace(f.Type), "f32")
		salt := fieldSalt(name)
		words := make([]uint32, count)
		for i := 0; i < count; i++ {
			v := ic.valueAt(i, count, salt)
			if isF32 {
				words[i] = math.Float32bits(float32(v))
			} else {
				words[i] = uint32(int32(v))
			}
		}
		out = append(out, SeedWrite{At: fmt.Sprintf("0x%X", f.Offset), U32: words})
	}
	return out
}

// valueAt computes element i (of count) of a FieldInit's distribution.
func (ic FieldInit) valueAt(i, count int, salt uint32) float64 {
	switch strings.ToLower(strings.TrimSpace(ic.Dist)) {
	case "const":
		return ic.Value
	case "uniform":
		return ic.Min + (ic.Max-ic.Min)*hashUnit(uint32(i)+salt)
	case "spread":
		if count > 1 {
			return ic.Min + (ic.Max-ic.Min)*float64(i)/float64(count-1)
		}
		return ic.Min
	default: // "zero" / ""
		return 0
	}
}

// hashUnit maps an index to a deterministic pseudo-random value in [0,1) (a splitmix32
// finalizer), so a "uniform" scatter is reproducible across boots and independent of any
// runtime RNG.
func hashUnit(x uint32) float64 {
	x += 0x9E3779B9
	x = (x ^ (x >> 16)) * 0x21F0AAAD
	x = (x ^ (x >> 15)) * 0x735A2D97
	x = x ^ (x >> 15)
	return float64(x) / float64(1<<32)
}

// fieldSalt derives a per-field offset for the index hash (FNV-1a of the name), so two
// uniform fields (x and y) scatter independently rather than along a diagonal.
func fieldSalt(name string) uint32 {
	var h uint32 = 2166136261
	for i := 0; i < len(name); i++ {
		h = (h ^ uint32(name[i])) * 16777619
	}
	return h
}

// FieldNames returns the set of canonical projected field names, for resolving ports.
func (m *SystemModel) FieldNames() map[string]bool {
	names := map[string]bool{}
	for _, f := range m.ProjectFields() {
		names[f.Name] = true
	}
	return names
}

// ResolvePort maps a (possibly non-canonical) port token to the canonical projected field
// name(s) it means, or nil if it names nothing in the model. It handles: an entity name or
// "<entity>_state"/"<entity>_states" → all that entity's projected fields; "<entity>_<field>"
// → that field; a bare "<field>" that matches exactly one entity → that entity's field.
// This is how a component that declared "particle_states" or bare "x" is pointed at the
// real modeled state instead of minting a phantom contract field.
func (m *SystemModel) ResolvePort(token string) []string {
	if m == nil {
		return nil
	}
	tok := strings.TrimSpace(strings.ToLower(token))
	if tok == "" {
		return nil
	}
	entityFields := func(e Entity) []string {
		var fs []string
		for _, f := range e.Fields {
			if strings.TrimSpace(f.Name) != "" {
				fs = append(fs, e.Name+"_"+f.Name)
			}
		}
		return fs
	}
	// Whole-entity references: "particle", "particle_state", "particle_states".
	for _, e := range m.Entities {
		en := strings.ToLower(e.Name)
		if tok == en || tok == en+"_state" || tok == en+"_states" {
			return entityFields(e)
		}
	}
	// "<entity>_<field>" — a specific field, canonical or not.
	for _, e := range m.Entities {
		en := strings.ToLower(e.Name)
		if !strings.HasPrefix(tok, en+"_") {
			continue
		}
		suffix := tok[len(en)+1:]
		for _, f := range e.Fields {
			if strings.EqualFold(f.Name, suffix) {
				return []string{e.Name + "_" + f.Name}
			}
		}
	}
	// Bare "<field>" matching exactly one entity's field → prefix it; ambiguous → nil.
	var hits []string
	for _, e := range m.Entities {
		for _, f := range e.Fields {
			if strings.EqualFold(f.Name, tok) {
				hits = append(hits, e.Name+"_"+f.Name)
			}
		}
	}
	if len(hits) == 1 {
		return hits
	}
	return nil
}

// CollectionEntity returns the sole multi-instance entity, or nil if there is not exactly
// one. A map/leaf composition iterates that entity, so its per-element state is that
// entity's fields — this lets a leaf's element layout come from the model (the source of
// truth) rather than from unreliable declared port strings.
func (m *SystemModel) CollectionEntity() *Entity {
	if m == nil {
		return nil
	}
	var found *Entity
	for i := range m.Entities {
		if m.Entities[i].Cardinality > 1 {
			if found != nil {
				return nil // more than one collection — ambiguous
			}
			found = &m.Entities[i]
		}
	}
	return found
}

// Render formats the model for a design/authoring/critique prompt, or "" if empty.
func (m *SystemModel) Render() string {
	if m == nil || len(m.Entities) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("SYSTEM MODEL — the canonical entities and state this application is built from\n")
	b.WriteString("(the contract and every component port derive from THESE names and types):\n")
	for _, e := range m.Entities {
		card := e.Cardinality
		if card < 1 {
			card = 1
		}
		inst := "singleton"
		if card > 1 {
			inst = fmt.Sprintf("×%d", card)
		}
		fmt.Fprintf(&b, "- %s (%s)", e.Name, inst)
		if e.Purpose != "" {
			fmt.Fprintf(&b, " — %s", e.Purpose)
		}
		b.WriteString("\n")
		for _, f := range e.Fields {
			fmt.Fprintf(&b, "    %s : %s", f.Name, projElemType(f.Type))
			if f.Desc != "" {
				fmt.Fprintf(&b, " — %s", f.Desc)
			}
			b.WriteString("\n")
		}
	}
	if len(m.Dynamics) > 0 {
		b.WriteString("Dynamics (each tick, in order):\n")
		for i, d := range m.Dynamics {
			fmt.Fprintf(&b, "  %d. %s\n", i+1, d)
		}
	}
	if len(m.Init) > 0 {
		b.WriteString("Initial conditions (the boot state):\n")
		for _, ic := range m.Init {
			d := strings.ToLower(strings.TrimSpace(ic.Dist))
			switch d {
			case "const":
				fmt.Fprintf(&b, "  - %s.%s = %g\n", ic.Entity, ic.Field, ic.Value)
			case "uniform", "spread":
				fmt.Fprintf(&b, "  - %s.%s %s over [%g, %g]\n", ic.Entity, ic.Field, d, ic.Min, ic.Max)
			default:
				fmt.Fprintf(&b, "  - %s.%s = 0\n", ic.Entity, ic.Field)
			}
		}
	}
	return b.String()
}

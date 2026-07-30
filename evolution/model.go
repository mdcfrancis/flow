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

// SystemModel is the canonical model of an application: its entities and the per-tick
// dynamics that transform their state. The contract and ports are derived from it.
type SystemModel struct {
	Namespace string   `json:"namespace"`
	Objective string   `json:"objective"`
	Entities  []Entity `json:"entities"`
	Dynamics  []string `json:"dynamics,omitempty"` // per-tick transformations, in the model's vocabulary
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
	return b.String()
}

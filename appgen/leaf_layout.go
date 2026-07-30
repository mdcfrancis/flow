package appgen

import (
	"strings"

	"github.com/mdcfrancis/flow/evolution"
	"github.com/mdcfrancis/flow/flux"
	"github.com/mdcfrancis/flow/storage"
)

// LeafElementLayout returns the ELEMENT layout for a combinator LEAF, or nil if the cell
// is not one. A map leaf is handed a pointer to ONE element (argPtr = param 0); so its
// per-element state — the fields it WRITES (this particle's x/y/vx/vy) — are Elem fields
// at packed arg-pointer offsets (0,4,8,…), while the fields it READS but does not write
// are the shared GLOBALS (an attractor, screen size) at their absolute contract offsets.
//
// With this layout the leaf authors macro-WAT over its own element — (set vx (f32.add
// (get vx) …)), (set x (i32.add (get x) (get vx))) — reading globals absolutely, instead
// of being handed the whole global array layout (which is semantically wrong for a leaf,
// so synthesis bailed to raw WAT and copied the draw-stream doc). The element-field ORDER
// follows the leaf's declared writes (a v1 convention; a future composition element schema
// would make the interleaving authoritative).
func LeafElementLayout(ledger *storage.LedgerEngine, leafURN string) flux.Layout {
	ns := evolution.AppNamespaceOf(leafURN)
	env := LoadEnvelope(ledger, ns)
	if env == nil || !isCompositionLeaf(env, leafURN) {
		return nil
	}
	var leaf *Subsystem
	for i := range env.SubsystemRequirements {
		if env.SubsystemRequirements[i].Identity == leafURN {
			leaf = &env.SubsystemRequirements[i]
			break
		}
	}
	if leaf == nil {
		return nil
	}
	global := evolution.LayoutFromContract(evolution.LoadContract(ledger, ns))
	if global == nil {
		return nil
	}
	// MODEL-DRIVEN element layout: a map/leaf iterates the model's COLLECTION entity, so
	// its per-element state IS that entity's fields — taken from the model (the source of
	// truth) with the model's element types, not from the leaf's declared port strings
	// (which may be bare/mismatched). This is what guarantees the nebula leaf gets f32
	// element fields for particle x/y/vx/vy so its float physics type-checks.
	if m := evolution.LoadModel(ledger, ns); m != nil {
		if e := m.CollectionEntity(); e != nil {
			if layout := elementLayoutForEntity(e, global); layout != nil {
				return layout
			}
		}
	}
	if len(leaf.Writes) == 0 {
		return nil
	}
	layout := flux.Layout{}
	writes := map[string]bool{}
	var off uint32
	for _, w := range leaf.Writes {
		writes[w] = true
		// An element field is one scalar of the interleaved element buffer. Its type is
		// the array's ELEMENT type: an f32[] contract field (particle positions) yields an
		// f32 element so the leaf's physics is native float; an i32[] array (or scalar)
		// yields i32. Getting this right is what lets (set vx (f32.add (get vx) …)) type-
		// check instead of failing "expected f32, got i32" on an i32 element field.
		t := flux.TInt
		if gf, ok := global[w]; ok {
			if gf.Type == flux.TBuffer {
				if gf.EType == flux.TFloat {
					t = flux.TFloat
				}
			} else {
				t = gf.Type
			}
		}
		layout[w] = flux.Field{Type: t, Offset: off, Elem: true}
		off += 4
	}
	for _, r := range leaf.Reads {
		if writes[r] {
			continue
		}
		if gf, ok := global[r]; ok {
			layout[r] = gf // shared global input, absolute offset
		}
	}
	return layout
}

// elementLayoutForEntity builds a leaf's element layout from the model's collection entity:
// each of the entity's fields is an ELEMENT field (arg-pointer-relative, packed at 0,4,8,…)
// named by its canonical projected name and typed from the model (f32 for a continuous
// field), while every OTHER contract field the leaf reads (a singleton attractor, screen
// bounds) stays an absolute global. The element FIELD ORDER follows the entity's declared
// field order — the model's canonical layout, not a guess. Returns nil if the entity has no
// fields.
func elementLayoutForEntity(e *evolution.Entity, global flux.Layout) flux.Layout {
	layout := flux.Layout{}
	var off uint32
	elem := map[string]bool{}
	for _, f := range e.Fields {
		name := e.Name + "_" + f.Name
		t := flux.TInt
		if projFloat(f.Type) {
			t = flux.TFloat
		}
		layout[name] = flux.Field{Type: t, Offset: off, Elem: true}
		elem[name] = true
		off += 4
	}
	if len(layout) == 0 {
		return nil
	}
	// Every OTHER contract field — the singleton attractor, screen bounds, the count — is an
	// absolute GLOBAL the leaf may reference. Expose ALL of them (not just leaf.Reads): the
	// leaf reasons over the whole shared state, and a global the model needs but the port
	// list happened to omit must still resolve (otherwise the leaf's macro-WAT fails to
	// expand with "unknown field"). Element fields shadow their same-named array globals.
	for name, gf := range global {
		if !elem[name] {
			layout[name] = gf
		}
	}
	return layout
}

func projFloat(t string) bool {
	return strings.EqualFold(strings.TrimSpace(t), "f32")
}

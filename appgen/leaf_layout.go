package appgen

import (
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
	if leaf == nil || len(leaf.Writes) == 0 {
		return nil
	}
	global := evolution.LayoutFromContract(evolution.LoadContract(ledger, ns))
	if global == nil {
		return nil
	}
	layout := flux.Layout{}
	writes := map[string]bool{}
	var off uint32
	for _, w := range leaf.Writes {
		writes[w] = true
		// An element field is one scalar of the interleaved element buffer: a TBuffer
		// (array) contract field contributes an i32 element; a scalar keeps its type.
		t := flux.TInt
		if gf, ok := global[w]; ok && gf.Type != flux.TBuffer {
			t = gf.Type
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

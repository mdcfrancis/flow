package evolution

import (
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mdcfrancis/flow/compiler"
	"github.com/mdcfrancis/flow/flux"
	"github.com/mdcfrancis/flow/storage"
)

// The viewport projects its four shared fields (world/screen extent, with inits), and the
// generated to-screen macros + a WORLD-SPACE renderer that maps positions to pixels through
// them must expand and assemble — the whole logical→physical mapping, end to end.
func TestViewportProjectionAndMacros(t *testing.T) {
	m := &SystemModel{
		Viewport: &Viewport{WorldW: 1000, WorldH: 1000, ScreenW: 320, ScreenH: 240},
		Entities: []Entity{{Name: "particle", Cardinality: 50, Fields: []EntityField{
			{Name: "x", Type: "f32"}, {Name: "y", Type: "f32"}}}},
	}
	c := &AppContract{Fields: m.ProjectFields()}
	byName := map[string]ContractField{}
	for _, f := range c.Fields {
		byName[f.Name] = f
	}
	for _, n := range []string{"world_w", "world_h", "screen_w", "screen_h"} {
		if _, ok := byName[n]; !ok {
			t.Fatalf("viewport field %s not projected", n)
		}
	}
	if byName["world_w"].Init != 1000 || byName["screen_w"].Init != 320 {
		t.Errorf("viewport inits wrong: world_w=%d screen_w=%d", byName["world_w"].Init, byName["screen_w"].Init)
	}
	var prologue strings.Builder
	for _, pm := range m.ViewportMacros() {
		prologue.WriteString(pm.Src)
		prologue.WriteByte('\n')
	}
	src := prologue.String() + `(cell render-frame
  (for $i (get particle_count)
    (draw (circle (to_screen_x (atidx particle_x $i)) (to_screen_y (atidx particle_y $i))
                  (i32.const 3) (i32.const 0xFFFFFFFF)))))`
	wat, err := flux.Expand(src, LayoutFromContract(c))
	if err != nil {
		t.Fatalf("world-space renderer with to-screen did not expand: %v", err)
	}
	if !strings.Contains(wat, "i32.trunc_f32_s") {
		t.Errorf("to-screen should truncate a world f32 to an i32 pixel:\n%s", wat)
	}
	if art, cerr := compiler.NewCompilerService().CompileGenotype(wat); cerr != nil || art == nil || !art.SyntaxPassed {
		t.Fatalf("world-space renderer must assemble: %v", cerr)
	}
}

func TestSystemModelRoundTrip(t *testing.T) {
	le, err := storage.NewLedgerEngine(filepath.Join(t.TempDir(), "hdm.db"))
	if err != nil {
		t.Fatal(err)
	}
	ns := "urn:hdm:apps:nebula"
	m := &SystemModel{
		Namespace: ns, Objective: "a particle nebula",
		Entities: []Entity{
			{Name: "particle", Cardinality: 100, Purpose: "a drifting speck", Fields: []EntityField{
				{Name: "x", Type: "f32"}, {Name: "vx", Type: "f32"}}},
			{Name: "attractor", Cardinality: 1, Fields: []EntityField{{Name: "x", Type: "f32"}}},
		},
		Dynamics: []string{"each particle: vx += (attractor.x - x)/K; x += vx"},
	}
	if err := SaveModel(le, ns, m); err != nil {
		t.Fatal(err)
	}
	got := LoadModel(le, ns)
	if got == nil || len(got.Entities) != 2 || got.Entities[0].Cardinality != 100 {
		t.Fatalf("round-trip lost the model: %+v", got)
	}
}

// The projection is the ONE rule that turns the model into the contract: a multi-instance
// entity's fields become arrays of its cardinality plus a count; a singleton stays scalar.
// Names and types are canonical — this is what makes drift impossible.
func TestModelProjectsToContract(t *testing.T) {
	m := &SystemModel{Entities: []Entity{
		{Name: "particle", Cardinality: 100, Fields: []EntityField{
			{Name: "x", Type: "f32"}, {Name: "y", Type: "f32"}, {Name: "vx", Type: "f32"}}},
		{Name: "attractor", Cardinality: 1, Fields: []EntityField{
			{Name: "x", Type: "f32"}, {Name: "y", Type: "f32"}}},
		{Name: "screen", Cardinality: 1, Fields: []EntityField{
			{Name: "width", Type: "i32"}}},
	}}
	got := map[string]string{}
	var counts int
	for _, f := range m.ProjectFields() {
		got[f.Name] = f.Type
		if f.Name == "particle_count" {
			counts++
			if f.Init != 100 {
				t.Errorf("particle_count init should equal cardinality, got %d", f.Init)
			}
		}
	}
	want := map[string]string{
		"particle_count": "i32", "particle_x": "f32[100]", "particle_y": "f32[100]", "particle_vx": "f32[100]",
		"attractor_x": "f32", "attractor_y": "f32", "screen_width": "i32",
	}
	for n, ty := range want {
		if got[n] != ty {
			t.Errorf("field %s: got %q want %q", n, got[n], ty)
		}
	}
	if counts != 1 {
		t.Errorf("expected exactly one count field (only for the multi-instance entity), got %d", counts)
	}
	// A singleton must NOT get a count field.
	if _, ok := got["attractor_count"]; ok {
		t.Error("singleton entity should not project a count field")
	}
	// No phantom names — every projected field traces to an entity.field.
	if _, ok := got["particle_states"]; ok {
		t.Error("projection invented a phantom field")
	}
}

// A collection entity projects to one DENSE array column per field, laid out contiguously
// after its count — the layout a plain looping cell updates in place (no map combinator,
// so no need to interleave records).
func TestModelProjectsCollectionColumns(t *testing.T) {
	m := &SystemModel{Entities: []Entity{
		{Name: "particle", Cardinality: 100, Fields: []EntityField{
			{Name: "x", Type: "f32"}, {Name: "y", Type: "f32"}, {Name: "vx", Type: "f32"}, {Name: "vy", Type: "f32"}}},
		{Name: "attractor", Cardinality: 1, Fields: []EntityField{{Name: "x", Type: "f32"}}},
	}}
	fields := map[string]ContractField{}
	for _, f := range m.ProjectFields() {
		fields[f.Name] = f
	}
	// Each column is a dense f32[100] (400 bytes), contiguous.
	base := fields["particle_x"].Offset
	for j, name := range []string{"particle_x", "particle_y", "particle_vx", "particle_vy"} {
		f := fields[name]
		if f.Offset != base+j*400 {
			t.Errorf("%s offset: got 0x%X want 0x%X (dense columns)", name, f.Offset, base+j*400)
		}
		if f.Stride != 0 {
			t.Errorf("%s must be a dense array (stride 0), got %d", name, f.Stride)
		}
		if f.Type != "f32[100]" {
			t.Errorf("%s type: got %q want f32[100]", name, f.Type)
		}
	}
	// attractor starts after the four particle columns.
	if fields["attractor_x"].Offset != base+4*400 {
		t.Errorf("attractor_x offset: got 0x%X want 0x%X", fields["attractor_x"].Offset, base+4*400)
	}
	// No interleaved-buffer handle or scratch map-out — collections are looped, not mapped.
	if _, ok := fields["particle"]; ok {
		t.Error("dense projection must not emit an interleaved buffer handle")
	}
	if _, ok := fields["particle_mapout"]; ok {
		t.Error("dense projection must not emit a scratch map-out")
	}
}

// Designed initial conditions expand to per-element boot seeds: a uniform scatter fills a
// collection's array with DISTINCT values in range (not all zero), x and y scatter
// independently (not a diagonal), a const singleton is a single value, and an f32 field is
// seeded as float bits.
func TestInitialSeedsScatter(t *testing.T) {
	m := &SystemModel{
		Entities: []Entity{
			{Name: "particle", Cardinality: 100, Fields: []EntityField{
				{Name: "x", Type: "f32"}, {Name: "y", Type: "f32"}}},
			{Name: "attractor", Cardinality: 1, Fields: []EntityField{{Name: "x", Type: "f32"}}},
		},
		Init: []FieldInit{
			{Entity: "particle", Field: "x", Dist: "uniform", Min: 0, Max: 320},
			{Entity: "particle", Field: "y", Dist: "uniform", Min: 0, Max: 240},
			{Entity: "attractor", Field: "x", Dist: "const", Value: 160},
		},
	}
	c := &AppContract{Fields: m.ProjectFields()}
	seeds := map[string][]uint32{}
	for _, s := range m.InitialSeeds(c) {
		seeds[s.At] = s.U32
	}
	at := func(name string) []uint32 {
		for _, f := range c.Fields {
			if f.Name == name {
				return seeds[fmt.Sprintf("0x%X", f.Offset)]
			}
		}
		return nil
	}
	px, py := at("particle_x"), at("particle_y")
	if len(px) != 100 || len(py) != 100 {
		t.Fatalf("expected 100-element scatter, got px=%d py=%d", len(px), len(py))
	}
	distinct := map[uint32]bool{}
	sameAsY := 0
	for i := 0; i < 100; i++ {
		distinct[px[i]] = true
		if v := math.Float32frombits(px[i]); v < 0 || v > 320 {
			t.Fatalf("scatter value %g out of [0,320]", v)
		}
		if px[i] == py[i] {
			sameAsY++
		}
	}
	if len(distinct) < 90 {
		t.Errorf("scatter should be mostly distinct, got %d distinct of 100", len(distinct))
	}
	if sameAsY > 5 {
		t.Errorf("x and y should scatter independently, but %d elements matched (diagonal)", sameAsY)
	}
	if ax := at("attractor_x"); len(ax) != 1 || math.Float32frombits(ax[0]) != 160 {
		t.Errorf("attractor_x const init should be 160.0, got %v", ax)
	}
}

// A non-i32/f32 element type is clamped (the substrate only addresses those two).
func TestModelProjectionClampsType(t *testing.T) {
	m := &SystemModel{Entities: []Entity{
		{Name: "cell", Cardinality: 1, Fields: []EntityField{{Name: "flag", Type: "bool"}}},
	}}
	for _, f := range m.ProjectFields() {
		if f.Name == "cell_flag" && f.Type != "i32" {
			t.Fatalf("unknown element type should clamp to i32, got %q", f.Type)
		}
	}
}

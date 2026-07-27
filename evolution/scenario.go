package evolution

// Scenario tests are the behavioral extension of acceptance suites: instead of
// a scalar input and a scalar result, a scenario SEEDS arbitrary shared-memory
// regions — crucially the monadic interfaces (the HMI input register, the
// inbound frame) and any prior state — runs the cell one or more times, and
// asserts on the OBSERVABLE output: the return value and the vector draw stream.
//
// This is how the loop verifies things like "keyboard input moves the player":
// the monadic boundaries are mocked simply by writing their memory regions in
// the deterministic shadow sandbox (host functions are already stubbed by
// installHostStubs), and behavior is checked black-box through the draw stream —
// never through a cell's private memory layout, which is unknowable a priori.

import (
	"context"
	"encoding/binary"
	"fmt"
	"strconv"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

// Shadow canvas geometry, mirroring execution's memory map. Kept local so the
// evolution package need not depend on
// the hypervisor; if the live layout moves, update both (and the docs).
const (
	scenarioCanvasBase uint32 = 0x51000
	scenarioCanvasCap  uint32 = 0xB0000 - 0x51000
)

// SeedWrite populates a shared-memory region before the cell runs. At is a
// byte offset (decimal or 0x-prefixed hex); U32 is a little-endian word run.
// This is the mock: e.g. At "0x50000" seeds the HMI input register.
type SeedWrite struct {
	At  string   `json:"at,omitempty"`
	U32 []uint32 `json:"u32,omitempty"`
	// Field names a shared-state contract field (e.g. "player_x") as an alias for
	// its offset. authorCoordination resolves it to At against the contract, so a
	// coordination scenario can't reference an invented offset — it can only name
	// a field the contract actually defines.
	Field string `json:"field,omitempty"`
	// Cmp applies only to a `reads` postcondition (ignored for seeds): how the
	// value AFTER the run is compared. "" or "eq" = exact match against U32 (all
	// words). Relative to the value BEFORE the run: "increased" / "decreased" /
	// "changed" / "unchanged". Threshold against U32[0]: "gt" / "lt" / "ge" / "le"
	// / "ne". Relative/threshold ops act on the first word only. This lets a
	// coordination check assert INTENT ("player_x increased") instead of an
	// arbitrary exact value ("player_x==105"), so any correct-direction cell passes.
	Cmp string `json:"cmp,omitempty"`
}

// DrawExpect asserts on the emitted vector draw stream. All set fields must hold.
// Layer/Op filter the records
// considered; the remaining checks apply to that filtered set.
type DrawExpect struct {
	Layer      *int   `json:"layer,omitempty"` // compositing layer (op high byte)
	Op         string `json:"op,omitempty"`    // rect|line|circle|any (default any)
	MinRecords int    `json:"minRecords,omitempty"`
	// MinColors requires at least this many DISTINCT rgba values among the records —
	// a flat fill has 1, a real data visualization (a gradient/heatmap) has many. The
	// draw-stream analogue of trajectory MinDistinct; catches a renderer that "draws
	// something" without actually painting the data.
	MinColors int `json:"minColors,omitempty"`
	// MinBrightSpread requires the drawn colors to span at least this much BRIGHTNESS
	// (max luma − min luma, 0-255) — so the palette actually maps the data to visible
	// CONTRAST (a dark interior AND bright regions), not a near-uniform fill that has
	// many distinct-but-similar colors. A blank/flat render fails this even when it has
	// enough distinct colors to pass MinColors. Direction-agnostic (any high-contrast
	// gradient passes), so it forces a visible structure without dictating the palette.
	MinBrightSpread int    `json:"minBrightSpread,omitempty"`
	Moved           string `json:"moved,omitempty"` // left|right|up|down (needs steps>=2)
	// NearX/NearY assert POSITION: at least one drawn primitive's anchor (a=x,
	// b=y) must be within drawPosTol of this coordinate. Paired with a seed of the
	// matching contract field, this forces the renderer to READ shared state and
	// draw the sprite THERE — e.g. seed player_x=250, expect a rect nearX=250 — so
	// the sprite actually moves with the state instead of being drawn statically.
	NearX *int `json:"nearX,omitempty"`
	NearY *int `json:"nearY,omitempty"`
}

// drawPosTol is how close (in canvas px) a primitive's anchor must be to a NearX/
// NearY target — roughly a sprite's half-extent, so drawing the sprite at the
// field value (as its top-left or centre) satisfies it.
const drawPosTol = 24

// ScenarioExpect is the assertion side of a scenario. Any set field must hold.
type ScenarioExpect struct {
	Result *int32      `json:"result,omitempty"`
	Draw   *DrawExpect `json:"draw,omitempty"`
	// Reads asserts shared-memory contents AFTER the run — the postcondition on
	// the shared-state contract. This is what lets a scenario verify cross-cell
	// coordination: e.g. "after a right-key event, player_x (at the contract
	// offset) equals 4". Each entry's At/U32 give an offset and the expected
	// little-endian words there.
	Reads []SeedWrite `json:"reads,omitempty"`
	// Trajectory asserts on the SEQUENCE of a field's values across a multi-step
	// run — properties a before/after comparison cannot express. This is what
	// makes emergent multi-tick dynamics verifiable (and un-gameable by jitter or
	// snap): a single-comparison "changed" check passes if the field wobbles once,
	// but a trajectory check sees the whole path.
	Trajectory []TrajectoryExpect `json:"trajectory,omitempty"`
}

// TrajectoryExpect asserts on the per-step sequence of one field's values.
type TrajectoryExpect struct {
	At string `json:"at"`
	// MinDistinct: the field must take at least this many DISTINCT values over the
	// run — freeze → 1, jitter between two → 2, real traversal → many. Compared as
	// raw words, so it is representation-agnostic (int or float bits alike).
	MinDistinct int `json:"minDistinct,omitempty"`
	// InBoundsMin/InBoundsMax, when set, require every step's value (as signed
	// int32) to lie within [min,max] — a sprite must never leave the window. Only
	// meaningful for integer-valued fields; omit for float-encoded state.
	InBoundsMin *int32 `json:"inBoundsMin,omitempty"`
	InBoundsMax *int32 `json:"inBoundsMax,omitempty"`
}

// Scenario is one behavioral case: seed memory, run Entry Steps times, assert.
type Scenario struct {
	Name   string         `json:"name"`
	Seed   []SeedWrite    `json:"seed,omitempty"`
	Steps  int            `json:"steps,omitempty"` // default 1
	Entry  string         `json:"entry,omitempty"` // run-tick|render-frame; default run-tick
	Args   []uint64       `json:"args,omitempty"`
	Expect ScenarioExpect `json:"expect"`
}

// DrawRecord is one decoded 24-byte draw-stream primitive.
type DrawRecord struct {
	Layer, Op  int32
	A, B, C, D int32
	RGBA       uint32
}

// decodeDrawStream parses a raw draw stream into records, stopping at a zero op.
func decodeDrawStream(buf []byte) []DrawRecord {
	var recs []DrawRecord
	for o := 0; o+24 <= len(buf); o += 24 {
		op := int32(binary.LittleEndian.Uint32(buf[o:]))
		if op == 0 {
			break
		}
		recs = append(recs, DrawRecord{
			Layer: (op >> 8) & 0xFF,
			Op:    op & 0xFF,
			A:     int32(binary.LittleEndian.Uint32(buf[o+4:])),
			B:     int32(binary.LittleEndian.Uint32(buf[o+8:])),
			C:     int32(binary.LittleEndian.Uint32(buf[o+12:])),
			D:     int32(binary.LittleEndian.Uint32(buf[o+16:])),
			RGBA:  binary.LittleEndian.Uint32(buf[o+20:]),
		})
	}
	return recs
}

// execScenario runs a scenario in a fresh deterministic shadow sandbox: it
// installs the standard host stubs, seeds the requested memory regions, calls
// the entry Steps times (state persists across calls, as in production), and —
// for render-frame — captures the decoded draw stream after each step. Guest
// traps and host panics are recovered into an error.
func execScenario(ctx context.Context, bytecode []byte, sc Scenario, payloadOffset, stateWindow uint32, resolver CellResolver, mask ...*FieldMask) (results []uint64, frames [][]DrawRecord, reads, pre [][]uint32, traj [][]uint32, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("scenario execution panic: %v", r)
		}
	}()

	r := wazero.NewRuntime(ctx)
	defer r.Close(ctx)
	if err = installHostStubs(ctx, r, replayEnv{payloadOffset: payloadOffset, stateWindow: stateWindow, resolver: resolver}); err != nil {
		return nil, nil, nil, nil, nil, err
	}
	mod, err := r.Instantiate(ctx, bytecode)
	if err != nil {
		return nil, nil, nil, nil, nil, fmt.Errorf("instantiate cell: %w", err)
	}
	mem := mod.Memory()
	if mem == nil {
		return nil, nil, nil, nil, nil, fmt.Errorf("cell exposes no memory")
	}

	// Seed the monadic interfaces / prior state.
	for _, s := range sc.Seed {
		off, perr := strconv.ParseUint(s.At, 0, 32)
		if perr != nil {
			return nil, nil, nil, nil, nil, fmt.Errorf("bad seed offset %q: %w", s.At, perr)
		}
		b := make([]byte, 4*len(s.U32))
		for i, w := range s.U32 {
			binary.LittleEndian.PutUint32(b[i*4:], w)
		}
		if len(b) > 0 && !mem.Write(uint32(off), b) {
			return nil, nil, nil, nil, nil, fmt.Errorf("seed of %d bytes does not fit at 0x%X", len(b), off)
		}
	}

	entry := sc.Entry
	if entry == "" {
		entry = EntryPoint
	}
	args := sc.Args
	if len(args) == 0 {
		if entry == "render-frame" {
			args = []uint64{uint64(scenarioCanvasBase), uint64(scenarioCanvasCap)}
		} else {
			args = []uint64{uint64(payloadOffset), 0}
		}
	}
	fn := mod.ExportedFunction(entry)
	if fn == nil {
		return nil, nil, nil, nil, nil, fmt.Errorf("entry %q not exported", entry)
	}
	steps := sc.Steps
	if steps < 1 {
		steps = 1
	}
	// Baseline: the values at each read offset BEFORE the run, so a relative
	// assertion (increased/decreased/changed) has something to compare against.
	pre = captureReads(mem, sc.Expect.Reads)

	// Trajectory capture: the per-step sequence of each tracked field's value,
	// starting from the pre-run value, so a whole-path assertion can be made.
	traj = make([][]uint32, len(sc.Expect.Trajectory))
	readWord := func(at string) uint32 {
		if off, perr := strconv.ParseUint(at, 0, 32); perr == nil {
			if buf, ok := mem.Read(uint32(off), 4); ok {
				return binary.LittleEndian.Uint32(buf)
			}
		}
		return 0
	}
	for k := range sc.Expect.Trajectory {
		traj[k] = append(traj[k], readWord(sc.Expect.Trajectory[k].At))
	}

	fm := firstMask(mask)
	for i := 0; i < steps; i++ {
		// BOUNDARY ENFORCEMENT (matches execTrampoline): before the step, snapshot the
		// non-writable ranges and zero the hidden (poison) ranges so an undeclared read
		// sees 0; after the step, restore the snapshot so a write outside the declared
		// ports does not persist. The trajectory/reads captured below therefore reflect
		// the SAME boundary the cell runs under live — a too-tight-ported cell reads as
		// static here and fails, instead of committing green and going static live.
		var maskSaved [][]byte
		if fm != nil {
			maskSaved = make([][]byte, len(fm.Revert))
			for k, r := range fm.Revert {
				if b, ok := mem.Read(r[0], r[1]); ok {
					c := make([]byte, len(b))
					copy(c, b)
					maskSaved[k] = c
				}
			}
			for _, p := range fm.Poison {
				if b, ok := mem.Read(p[0], p[1]); ok { // live view — zero in place
					for j := range b {
						b[j] = 0
					}
				}
			}
		}
		out, cerr := fn.Call(ctx, args...)
		for k, r := range fm.revertRanges() { // restore non-writable fields (undo stray writes)
			if k < len(maskSaved) && maskSaved[k] != nil {
				mem.Write(r[0], maskSaved[k])
			}
		}
		if cerr != nil {
			return nil, nil, nil, nil, nil, fmt.Errorf("scenario trap on step %d: %w", i, cerr)
		}
		results = out
		for k := range sc.Expect.Trajectory {
			traj[k] = append(traj[k], readWord(sc.Expect.Trajectory[k].At))
		}
		if entry == "render-frame" {
			var recs []DrawRecord
			if len(out) > 0 {
				if n := uint32(out[0]); n > 0 && n <= scenarioCanvasCap {
					if buf, ok := mem.Read(scenarioCanvasBase, n); ok {
						recs = decodeDrawStream(append([]byte(nil), buf...))
					}
				}
			}
			frames = append(frames, recs)
		}
	}
	// Capture the shared-memory postconditions (contract fields) after the run.
	reads = captureReads(mem, sc.Expect.Reads)
	return results, frames, reads, pre, traj, nil
}

// RunTrajectory runs a scenario and returns the captured per-step value sequence
// for each of its Trajectory entries — the observed PATH of each tracked field,
// for inspection or LLM judgment. One inner slice per Trajectory entry, in order.
func RunTrajectory(ctx context.Context, phenotype []byte, sc Scenario, payloadOffset, stateWindow uint32, resolver CellResolver, mask ...*FieldMask) ([][]uint32, error) {
	_, _, _, _, traj, err := execScenario(ctx, phenotype, sc, payloadOffset, stateWindow, resolver, mask...)
	return traj, err
}

// captureReads reads each check's offset out of memory into a word slice. For a
// relative op (no U32 threshold) it reads a single word so a before/after
// comparison is possible.
func captureReads(mem api.Memory, checks []SeedWrite) [][]uint32 {
	out := make([][]uint32, 0, len(checks))
	for _, rd := range checks {
		n := len(rd.U32)
		if n == 0 {
			n = 1 // relative op (increased/changed/…): read a single word
		}
		got := make([]uint32, n)
		if off, perr := strconv.ParseUint(rd.At, 0, 32); perr == nil {
			if buf, ok := mem.Read(uint32(off), uint32(4*n)); ok {
				for i := range got {
					got[i] = binary.LittleEndian.Uint32(buf[i*4:])
				}
			}
		}
		out = append(out, got)
	}
	return out
}

// matchRead evaluates one read postcondition against the value before/after the
// run. See SeedWrite.Cmp for the operators.
func matchRead(rd SeedWrite, before, after []uint32) bool {
	switch rd.Cmp {
	case "", "eq":
		if len(after) != len(rd.U32) {
			return false
		}
		for j, want := range rd.U32 {
			if after[j] != want {
				return false
			}
		}
		return true
	}
	if len(after) == 0 {
		return false
	}
	a := int32(after[0])
	switch rd.Cmp {
	case "increased":
		return len(before) > 0 && a > int32(before[0])
	case "decreased":
		return len(before) > 0 && a < int32(before[0])
	case "changed":
		return len(before) > 0 && a != int32(before[0])
	case "unchanged":
		return len(before) > 0 && a == int32(before[0])
	}
	if len(rd.U32) == 0 {
		return false
	}
	t := int32(rd.U32[0])
	switch rd.Cmp {
	case "gt":
		return a > t
	case "lt":
		return a < t
	case "ge":
		return a >= t
	case "le":
		return a <= t
	case "ne":
		return a != t
	}
	return false
}

// matchScenario reports whether the observed output satisfies the expectation.
func matchScenario(sc Scenario, results []uint64, frames [][]DrawRecord, reads, pre, traj [][]uint32) bool {
	ok, _ := matchScenarioReason(sc, results, frames, reads, pre, traj)
	return ok
}

// matchTrajectory evaluates one trajectory assertion against the per-step value
// sequence of a field.
func matchTrajectory(t TrajectoryExpect, seq []uint32) bool {
	if len(seq) == 0 {
		return false
	}
	if t.MinDistinct > 0 {
		seen := map[uint32]struct{}{}
		for _, v := range seq {
			seen[v] = struct{}{}
		}
		if len(seen) < t.MinDistinct {
			return false // froze (1) or jittered among too few values
		}
	}
	if t.InBoundsMin != nil || t.InBoundsMax != nil {
		for _, v := range seq {
			s := int32(v)
			if t.InBoundsMin != nil && s < *t.InBoundsMin {
				return false
			}
			if t.InBoundsMax != nil && s > *t.InBoundsMax {
				return false
			}
		}
	}
	return true
}

// primOf maps an op-name to its primitive code (0 => "any").
func primOf(op string) int32 {
	switch op {
	case "rect":
		return 1
	case "line":
		return 2
	case "circle":
		return 3
	default:
		return 0
	}
}

// matchDraw evaluates a draw-stream expectation against the captured frames.
// luma is the perceived brightness (0-255) of a 0xRRGGBBAA color, for contrast checks.
func luma(rgba uint32) int {
	r := int((rgba >> 24) & 0xff)
	g := int((rgba >> 16) & 0xff)
	b := int((rgba >> 8) & 0xff)
	return (r*30 + g*59 + b*11) / 100
}

func matchDraw(d DrawExpect, frames [][]DrawRecord) bool {
	if len(frames) == 0 {
		return false
	}
	wantPrim := primOf(d.Op)
	filter := func(recs []DrawRecord) []DrawRecord {
		out := recs[:0:0]
		for _, r := range recs {
			if d.Layer != nil && int(r.Layer) != *d.Layer {
				continue
			}
			if wantPrim != 0 && r.Op != wantPrim {
				continue
			}
			out = append(out, r)
		}
		return out
	}
	last := filter(frames[len(frames)-1])
	if d.MinRecords > 0 && len(last) < d.MinRecords {
		return false
	}
	if d.MinColors > 0 {
		seen := map[uint32]struct{}{}
		for _, r := range last {
			seen[r.RGBA] = struct{}{}
		}
		if len(seen) < d.MinColors {
			return false // too few distinct colors — a flat fill, not the data
		}
	}
	if d.MinBrightSpread > 0 {
		minL, maxL, any := 255, 0, false
		for _, r := range last {
			l := luma(r.RGBA)
			if l < minL {
				minL = l
			}
			if l > maxL {
				maxL = l
			}
			any = true
		}
		if !any || maxL-minL < d.MinBrightSpread {
			return false // colors don't span dark→bright — no visible contrast/structure
		}
	}
	// Position: some filtered primitive must be anchored near the target coord.
	if d.NearX != nil || d.NearY != nil {
		hit := false
		for _, r := range last {
			if d.NearX != nil && abs32(r.A-int32(*d.NearX)) > drawPosTol {
				continue
			}
			if d.NearY != nil && abs32(r.B-int32(*d.NearY)) > drawPosTol {
				continue
			}
			hit = true
			break
		}
		if !hit {
			return false
		}
	}
	if d.Moved != "" {
		if len(frames) < 2 {
			return false
		}
		first := filter(frames[0])
		if len(first) == 0 || len(last) == 0 {
			return false
		}
		fx, fy := centroid(first)
		lx, ly := centroid(last)
		const eps = 0.5
		switch d.Moved {
		case "right":
			return lx > fx+eps
		case "left":
			return lx < fx-eps
		case "down":
			return ly > fy+eps
		case "up":
			return ly < fy-eps
		default:
			return false
		}
	}
	return true
}

func abs32(v int32) int32 {
	if v < 0 {
		return -v
	}
	return v
}

// centroid returns the mean anchor (a,b) of a set of primitives.
func centroid(recs []DrawRecord) (x, y float64) {
	if len(recs) == 0 {
		return 0, 0
	}
	var sx, sy float64
	for _, r := range recs {
		sx += float64(r.A)
		sy += float64(r.B)
	}
	n := float64(len(recs))
	return sx / n, sy / n
}

// ScenarioPassFlags reports, per scenario, whether it passes — so callers can
// derive what a cell is VERIFIED to do (the application map) rather than what it
// claims. A scenario whose execution traps is a failure.
func ScenarioPassFlags(ctx context.Context, phenotype []byte, scenarios []Scenario, payloadOffset, stateWindow uint32, resolver CellResolver, mask ...*FieldMask) []bool {
	out := make([]bool, len(scenarios))
	for i, sc := range scenarios {
		results, frames, reads, pre, traj, err := execScenario(ctx, phenotype, sc, payloadOffset, stateWindow, resolver, mask...)
		if err != nil {
			continue
		}
		out[i] = matchScenario(sc, results, frames, reads, pre, traj)
	}
	return out
}

// ScenarioScore runs each scenario against the phenotype and counts how many
// pass. A scenario whose execution traps counts as a failure.
func ScenarioScore(ctx context.Context, phenotype []byte, scenarios []Scenario, payloadOffset, stateWindow uint32, resolver CellResolver, mask ...*FieldMask) (passed, total int) {
	total = len(scenarios)
	for _, sc := range scenarios {
		results, frames, reads, pre, traj, err := execScenario(ctx, phenotype, sc, payloadOffset, stateWindow, resolver, mask...)
		if err != nil {
			continue
		}
		if matchScenario(sc, results, frames, reads, pre, traj) {
			passed++
		}
	}
	return passed, total
}

// ScoreSuite grades a phenotype against both the scalar acceptance tests and the
// behavioral scenarios in a suite, returning the combined pass/total. This is
// the single scorer the orchestrator drives so UI (draw-stream) behavior and
// compute (int-in/int-out) behavior are graded uniformly.
func ScoreSuite(ctx context.Context, phenotype []byte, entry string, suite *AcceptanceSuite, payloadOffset, stateWindow uint32, resolver CellResolver, mask ...*FieldMask) (passed, total int) {
	if suite == nil {
		return 0, 0
	}
	// Scalar acceptance tests are pure payload int→int transforms that don't persist
	// shared state, so the boundary mask (which governs contract fields) does not
	// apply to them — only the behavioral scenarios are graded under the mask.
	tp, tt := AcceptanceScore(ctx, phenotype, entry, suite, payloadOffset, stateWindow, resolver)
	sp, st := ScenarioScore(ctx, phenotype, suite.Scenarios, payloadOffset, stateWindow, resolver, mask...)
	return tp + sp, tt + st
}

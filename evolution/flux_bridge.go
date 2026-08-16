package evolution

import (
	"fmt"
	"sort"
	"strings"

	"github.com/mdcfrancis/flow/compiler"
	"github.com/mdcfrancis/flow/flux"
	"github.com/mdcfrancis/flow/storage"
)

// extractMacroWAT isolates the model's macro-WAT program — the outermost balanced
// (cell …) or (module …) — tolerating markdown fences, <think> blocks, and surrounding
// prose. Returns "" if none.
func extractMacroWAT(resp string) string {
	s := stripThink(resp)
	best := ""
	for _, key := range []string{"(cell", "(module"} {
		start := strings.Index(s, key)
		if start < 0 {
			continue
		}
		depth, inStr := 0, false
		for i := start; i < len(s); i++ {
			c := s[i]
			switch {
			case inStr:
				if c == '"' {
					inStr = false
				}
			case c == '"':
				inStr = true
			case c == '(':
				depth++
			case c == ')':
				depth--
				if depth == 0 {
					cand := s[start : i+1]
					if best == "" || strings.HasPrefix(strings.TrimSpace(cand), "(cell") {
						best = cand
					}
					i = len(s)
				}
			}
		}
		if strings.HasPrefix(strings.TrimSpace(best), "(cell") {
			break // prefer a (cell …) macro program
		}
	}
	return best
}

// Flux DRAFT store: the most recent macro-WAT program the model authored for a cell,
// whether or not it committed. The committed genome is the source of truth (P0),
// but a cell shows its stub until a candidate commits — so for the console we also
// keep the latest draft, to display the macro-WAT the model is actively writing even
// while a cell is still building.
func fluxDraftRef(urn string) string { return urn + ":flux-draft" }

// SaveFluxDraft records the latest macro-WAT source authored for a cell.
func SaveFluxDraft(ledger *storage.LedgerEngine, urn, src string) error {
	if ledger == nil || strings.TrimSpace(src) == "" {
		return nil
	}
	h, err := ledger.WriteBlock([]byte(src))
	if err != nil {
		return err
	}
	return ledger.UpdateRef(fluxDraftRef(urn), h)
}

// LoadFluxDraft returns the latest Flux draft for a cell, or "".
func LoadFluxDraft(ledger *storage.LedgerEngine, urn string) string {
	if ledger == nil {
		return ""
	}
	h, err := ledger.GetRef(fluxDraftRef(urn))
	if err != nil {
		return ""
	}
	raw, err := ledger.ReadBlock(h)
	if err != nil {
		return ""
	}
	return string(raw)
}

// ClearFluxDraft drops a cell's working draft — the UNWIND: the next synthesis
// falls back to the last committed genome (a fresh branch) instead of continuing
// to refine a draft judged blocked.
func ClearFluxDraft(ledger *storage.LedgerEngine, urn string) {
	if ledger != nil {
		_ = ledger.UpdateRef(fluxDraftRef(urn), "")
	}
}

// lowerMacroToBytecode expands a macro-WAT draft to wasm bytecode (for judging a draft
// without committing it), or an error if it does not expand/assemble.
func lowerMacroToBytecode(layout flux.Layout, src string) ([]byte, error) {
	wat, err := flux.Expand(src, layout)
	if err != nil {
		return nil, err
	}
	art, aerr := compiler.NewCompilerService().CompileGenotype(wat)
	if aerr != nil || art == nil || !art.SyntaxPassed {
		return nil, fmt.Errorf("assemble: %v", aerr)
	}
	return art.Bytecode, nil
}

// This file bridges the macro-WAT surface into the operational synthesis path. When
// a contract layout is available, the sieve accepts a model that authors a (cell …)
// macro-WAT program: it is expanded against the layout to raw WAT here (flux.Expand),
// then verified by the identical downstream gates. A model that emits a bare (module …)
// is unaffected — flux.Expand leaves non-macro WAT untouched.

// hmiFields are the fixed HARDWARE CAPABILITY fields every cell may read: the HMI
// Input Event Register (execution.InputBase = 0x50000). They are read-only (the
// host writes them each tick) and exposed to Flux so a cell can respond to
// keyboard/mouse without inventing an offset. A cell only receives real values if
// its enforced boundary declares it reads "HMI input"; otherwise a read returns 0
// (the boundary's default), which is harmless. Offsets mirror the InMouseX… block
// in execution/hypervisor.go.
var hmiFields = map[string]flux.Field{
	"hmi_mouse_x":    {Type: flux.TInt, Offset: 0x50000, ReadOnly: true}, // cursor x (0..319)
	"hmi_mouse_y":    {Type: flux.TInt, Offset: 0x50004, ReadOnly: true}, // cursor y (0..239)
	"hmi_buttons":    {Type: flux.TInt, Offset: 0x50008, ReadOnly: true}, // held-button mask: bit0 L, bit1 R, bit2 M
	"hmi_modifiers":  {Type: flux.TInt, Offset: 0x5000C, ReadOnly: true}, // modifier mask: bit0 shift,1 ctrl,2 alt,3 meta
	"hmi_event_seq":  {Type: flux.TInt, Offset: 0x50010, ReadOnly: true}, // monotonic event counter (compare vs last tick)
	"hmi_event_type": {Type: flux.TInt, Offset: 0x50014, ReadOnly: true}, // 2 down,3 up,4 click,5 keydown,6 keyup
	"hmi_event_x":    {Type: flux.TInt, Offset: 0x50018, ReadOnly: true}, // cursor x at event time
	"hmi_event_y":    {Type: flux.TInt, Offset: 0x5001C, ReadOnly: true}, // cursor y at event time
	"hmi_key":        {Type: flux.TInt, Offset: 0x50020, ReadOnly: true}, // key code for key events
}

// LayoutFromContract turns the app's shared-state contract into a Flux field
// Layout (name → type + offset). Only the scalar types Flux v1 lowers are
// included; array/unknown fields are omitted (a Flux cell that reads one fails
// type-checking with a clear message, rather than lowering to bad addressing).
// Returns nil when nothing is addressable, which disables the Flux path.
func LayoutFromContract(c *AppContract) flux.Layout {
	if c == nil {
		return nil
	}
	l := flux.Layout{}
	for _, f := range c.Fields {
		ft := strings.TrimSpace(f.Type)
		switch {
		case ft == "i32":
			l[f.Name] = flux.Field{Type: flux.TInt, Offset: uint32(f.Offset)}
		case ft == "f32":
			l[f.Name] = flux.Field{Type: flux.TFloat, Offset: uint32(f.Offset)}
		case strings.HasPrefix(ft, "i32[") && strings.HasSuffix(ft, "]"):
			// An i32 array becomes a bounded Flux BUFFER (addressed with at/store,
			// bounds-clamped) — so an array-writing cell (a particle system, a grid
			// renderer) is Flux-addressable instead of falling back to raw WAT.
			if n := typeWords(ft); n > 1 {
				l[f.Name] = flux.Field{Type: flux.TBuffer, Offset: uint32(f.Offset), Len: uint32(n), EType: flux.TInt, Stride: uint32(f.Stride)}
			}
		case strings.HasPrefix(ft, "f32[") && strings.HasSuffix(ft, "]"):
			// An f32 array — the natural home for continuous per-element state (a
			// particle field's positions/velocities). Its elements are addressed as
			// native floats: (atidx) loads f32, (setidx) stores f32, (atidxi) truncates
			// to an i32 pixel coordinate. This is what lets a type critic move particle
			// state off i32 (where gravity/division floor to zero) onto real floats. A
			// non-zero Stride makes it one column of an interleaved collection buffer.
			if n := typeWords(ft); n > 1 {
				l[f.Name] = flux.Field{Type: flux.TBuffer, Offset: uint32(f.Offset), Len: uint32(n), EType: flux.TFloat, Stride: uint32(f.Stride)}
			}
		default:
			continue // unknown: not addressable by Flux yet
		}
	}
	if len(l) == 0 {
		return nil
	}
	return l
}

// fluxLayoutFor returns the Flux field layout for a cell's app when the Flux path
// is enabled and the app has an addressable contract, or nil (which disables the
// Flux path for that cell — it is synthesized as WAT, unchanged).
func (o *Orchestrator) fluxLayoutFor(urn string) flux.Layout {
	if !o.FluxEnabled {
		return nil
	}
	// A combinator LEAF gets an ELEMENT layout (per-element fields arg-pointer-relative),
	// not the global contract layout — otherwise (get x) reads the whole global array
	// instead of this element, and synthesis bails to raw WAT (a physics leaf that wrote
	// render records). Its element layout already includes the shared globals it reads.
	if o.LeafElementLayout != nil {
		if el := o.LeafElementLayout(urn); el != nil {
			return el
		}
	}
	l := LayoutFromContract(LoadContract(o.ledger, AppNamespaceOf(urn)))
	if l == nil {
		return nil // no addressable app state → Flux path off for this cell
	}
	// Every Flux cell may also read the fixed hardware capabilities (HMI input),
	// without a contract field clobber.
	for n, f := range hmiFields {
		if _, exists := l[n]; !exists {
			l[n] = f
		}
	}
	return l
}

// candidateWAT converts one model response into WAT for the sieve to assemble.
// With a layout and a Flux (cell …) form present, it lowers Flux → WAT and marks
// the source Flux; otherwise it extracts WAT as before. A Flux compile error is
// returned so the loop can feed the semantic message back for repair.
func candidateWAT(resp string, layout flux.Layout, prologue string) (wat, fluxSrc string, err error) {
	if layout != nil {
		// Macro-WAT: the model wrote (cell …) with field macros; expand it against the
		// contract to raw WAT. The application prologue (default + app-harvested macros)
		// is prepended so a call to (reflect …)/(clampi …) resolves; the model may also
		// define its own (defmacro …) inline. A bare (module …) passes straight through
		// (flux.Expand leaves non-macro WAT untouched). fluxSrc is the model's own program
		// (without the prologue) — the stored genome — so the prologue never bloats it.
		src := extractMacroWAT(resp)
		if src == "" {
			return extractWAT(resp), "", nil
		}
		w, cerr := flux.Expand(prologue+src, layout)
		if cerr != nil {
			return "", src, cerr
		}
		return w, src, nil
	}
	return extractWAT(resp), "", nil
}

// stripThink removes any <think>…</think> reasoning blocks a model may emit. The
// local server's enable_thinking:false suppresses these on the normal path, but this
// is a cheap belt-and-braces so a stray block never reaches the compiler.
func stripThink(s string) string {
	for {
		i := indexFold(s, "<think>")
		if i < 0 {
			return s
		}
		if j := indexFold(s[i:], "</think>"); j >= 0 {
			s = s[:i] + s[i+j+len("</think>"):]
		} else {
			return s[:i] // unclosed — drop the tail
		}
	}
}

func indexFold(s, sub string) int {
	return strings.Index(strings.ToLower(s), strings.ToLower(sub))
}


// macroSeedBlock renders the macro-WAT authoring instructions appended to the build
// seed: the macro forms the model uses over raw WAT, plus the cell's typed field
// list (so it names fields instead of computing offsets). The worked example is
// retrieved from the knowledge base and lazily inlined by renderKnowledge.
func macroSeedBlock(contract *EntryContract, layout flux.Layout) string {
	view := contract != nil && contract.Name == "render-frame"
	var stateFields, inputFields []string
	for n, f := range layout {
		entry := fmt.Sprintf("%s : %s", n, f.Type)
		if f.ReadOnly {
			inputFields = append(inputFields, entry)
		} else {
			stateFields = append(stateFields, entry)
		}
	}
	sort.Strings(stateFields)
	sort.Strings(inputFields)

	var b strings.Builder
	b.WriteString("\n=== OUTPUT FORMAT: MACRO-WAT (native WAT with field macros) ===\n")
	b.WriteString("Write the cell in WAT using these MACROS — do not hand-write the module, the memory\n")
	b.WriteString("import, or field offsets. Output ONLY a (cell …) form.\n\n")
	if view {
		b.WriteString("  (cell render-frame BODY…)           a UI cell; BODY draws, the cell returns the length.\n")
		b.WriteString("  (draw PRIM)                         append ONE shape. PRIM = (circle X Y R COLOR) |\n")
		b.WriteString("                                      (rect X Y W H COLOR) | (line X1 Y1 X2 Y2 COLOR); COLOR=(i32.const 0xRRGGBBAA).\n")
		b.WriteString("  (scene PRIM…)                       shorthand for several (draw …) in a row (a FIXED set of shapes).\n")
		b.WriteString("  For a VARIABLE number of shapes (one per ARRAY element), LOOP and draw:\n")
		b.WriteString("    (for $i (get count) (draw (circle (atidxi px $i) (atidxi py $i) (i32.const 3) COLOR)))\n")
	} else {
		b.WriteString("  (cell run-tick BODY…)               BODY ends with (i32.const 0).\n")
	}
	b.WriteString("  (get NAME) / (set NAME EXPR)        read / write a shared field (f32 if the field is f32, else i32)\n")
	b.WriteString("  (geti NAME)                         field NAME as an i32 (TRUNCATES an f32 field, for pixel coords)\n")
	b.WriteString("  (atidx NAME IDX) / (setidx NAME IDX EXPR)   ARRAY element read / write in its natural type (f32 for an f32[N] array); IDX may be a $loop var\n")
	b.WriteString("  (atidxi NAME IDX)                   ARRAY element as an i32 (TRUNCATES an f32[N] element — use for pixel coords)\n")
	b.WriteString("  (for $i COUNT BODY…)                run BODY for $i=0..COUNT-1 — update every array element, or draw one shape per element\n")
	b.WriteString("COLLECTIONS: a set of N entities is a group of array columns <entity>_<field> (e.g.\n")
	b.WriteString("  particle_x, particle_vx) plus a count <entity>_count. Update the collection IN PLACE\n")
	b.WriteString("  with ONE run-tick loop — read/write record i's field with (atidx/setidx <entity>_<field> $i):\n")
	b.WriteString("    (for $i (get particle_count) (setidx particle_vx $i (f32.add (atidx particle_vx $i) …)) (setidx particle_x $i (f32.add (atidx particle_x $i) (atidx particle_vx $i))))\n")
	b.WriteString("  Draw it with a render loop: (for $i (get particle_count) (draw (circle (atidxi particle_x $i) (atidxi particle_y $i) …))).\n")
	b.WriteString("  This IS how to handle a particle system / agents — a plain loop, NOT a map combinator.\n")
	b.WriteString("WORLD vs SCREEN: entity positions are WORLD-space (the simulation's own units), NOT pixels.\n")
	b.WriteString("  A renderer maps a world coordinate to a pixel with (to_screen_x WX) / (to_screen_y WY) — the\n")
	b.WriteString("  viewport transform — so draw at the MAPPED coord, never the raw world value:\n")
	b.WriteString("    (for $i (get particle_count) (draw (circle (to_screen_x (atidx particle_x $i)) (to_screen_y (atidx particle_y $i)) (i32.const 3) COLOR)))\n")
	b.WriteString("Everything else is ordinary WAT: i32.*/f32.* math, (local $t f32), etc. Locals are hoisted for you.\n")
	b.WriteString("USE FLOATS for continuous physics (a field typed f32): f32.div does not floor to zero.\n\n")
	fmt.Fprintf(&b, "SHARED STATE fields (read and write, within your enforced boundary above):\n  %s\n", strings.Join(stateFields, ", "))
	if len(inputFields) > 0 {
		fmt.Fprintf(&b, "INPUT fields (READ-ONLY hardware — the host writes them each tick; NEVER write them):\n  %s\n", strings.Join(inputFields, ", "))
		b.WriteString("  hmi_event_type: 2 mousedown, 3 mouseup, 4 click, 5 keydown, 6 keyup. Detect a NEW discrete event by comparing hmi_event_seq to the value you saw last tick. hmi_key is the key code; hmi_mouse_x/y is the live cursor; hmi_buttons/hmi_modifiers are bit masks.\n")
	}
	b.WriteString("\nOutput only your (cell …) program.\n")
	return b.String()
}

// macroCorrectionDirective renders the sieve repair prompt for a macro-WAT
// expand/assemble error — a semantic sentence, not a stack trace.
func macroCorrectionDirective(err error) string {
	return fmt.Sprintf(`[INNER SIEVE CORRECTION DIRECTIVE]
Your (cell …) program did not expand/assemble.
DEFECT: %v
Regenerate the complete (cell …) program, fixing exactly that. Output only the program.`, err)
}

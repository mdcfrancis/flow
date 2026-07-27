package evolution

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/mdcfrancis/flow/compiler"
	"github.com/mdcfrancis/flow/flux"
	"github.com/mdcfrancis/flow/macro"
	"github.com/mdcfrancis/flow/storage"
)

// fluxSurfaceName selects the SURFACE the operational sieve authors cells in:
// "sexpr" (default, the S-expression Flux) or "forth" (the type-stratified
// concatenative surface that measured a large LLM-efficiency win — see
// docs/flux-surface-ir.md). Both read to the same IR and lower through the identical
// invariant, so the choice only changes what the model generates. Opt-in via
// HDM_FLUX_SURFACE so default behavior is unchanged.
func fluxSurfaceName() string {
	// An explicit env override wins (for testing a surface without promoting it);
	// otherwise the promoted default from the ledger (currentSurface), set at boot by
	// InstallSurface and flipped by a successful PromoteSurface epoch.
	switch strings.ToLower(strings.TrimSpace(os.Getenv("HDM_FLUX_SURFACE"))) {
	case "forth":
		return "forth"
	case "sexpr":
		return "sexpr"
	}
	return currentSurface()
}

func fluxIsForth() bool { return fluxSurfaceName() == "forth" }

// isMacro reports whether the OPERATIONAL surface is macro-WAT — the default. The model
// writes native WAT with (cell)/(get)/(set)/(scene) macros (macro.Expand), rather than a
// Flux/Forth program. HDM_FLUX_SURFACE=forth|sexpr opts back into the (now legacy) Flux
// language path; anything else (unset, or "macro") is macro-WAT.
func isMacro() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("HDM_FLUX_SURFACE"))) {
	case "forth", "sexpr":
		return false
	}
	return true
}

// IsMacro reports whether the operational surface is macro-WAT (the default), for
// callers outside the package (appgen seeds a macro-WAT no-op in that case).
func IsMacro() bool { return isMacro() }

// macroFields projects the app's Flux layout to the macro field map (name → offset +
// whether it's an f32). Array (TBuffer) fields carry their base offset with i32 elements.
func macroFields(layout flux.Layout) map[string]macro.Field {
	m := make(map[string]macro.Field, len(layout))
	for name, f := range layout {
		m[name] = macro.Field{Offset: f.Offset, Float: f.Type == flux.TFloat}
	}
	return m
}

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

// ActiveSurface returns the operational synthesis surface (Forth by default), for
// callers outside the package — e.g. appgen seeds its no-op scaffold genome in this
// surface so a cell's stored draft matches the surface the model is asked to author in.
func ActiveSurface() flux.Surface { return activeSurface() }

// activeSurface returns the flux.Surface the operational sieve currently authors in.
func activeSurface() flux.Surface {
	if fluxIsForth() {
		return flux.Forth{}
	}
	return flux.SExpr{}
}

// Flux DRAFT store: the most recent Flux program the model authored for a cell,
// whether or not it committed. The committed genome is the source of truth (P0),
// but a cell shows its WAT stub until a Flux candidate commits — so for the
// console we also keep the latest draft, to display the Flux the model is
// actively writing even while a cell is still building.
func fluxDraftRef(urn string) string { return urn + ":flux-draft" }

// SaveFluxDraft records the latest Flux source authored for a cell.
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

// lowerFluxToBytecode lowers a Flux program to wasm bytecode (for judging a draft
// without committing it), or an error if it does not compile.
func lowerFluxToBytecode(layout flux.Layout, src string) ([]byte, error) {
	wat, err := flux.Compile("cell", src, layout)
	if err != nil {
		return nil, err
	}
	art, aerr := compiler.NewCompilerService().CompileGenotype(wat)
	if aerr != nil || art == nil || !art.SyntaxPassed {
		return nil, fmt.Errorf("assemble: %v", aerr)
	}
	return art.Bytecode, nil
}

// This file bridges the Flux functional IR (docs/functional-ir.md) into the
// operational synthesis path. When a layout is available, the sieve accepts a
// model that authors a Flux (cell …) program: it is parsed, type-checked, and
// lowered to WAT here, then verified by the identical downstream gates. A model
// that still emits raw WAT is unaffected — extractFlux returns "" and the WAT
// path runs exactly as before.

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
				l[f.Name] = flux.Field{Type: flux.TBuffer, Offset: uint32(f.Offset), Len: uint32(n)}
			}
		default:
			continue // f32 arrays / unknown: not addressable by Flux yet
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
func candidateWAT(resp string, layout flux.Layout) (wat, fluxSrc string, err error) {
	if layout != nil && isMacro() {
		// Macro-WAT (the default): the model wrote (cell …) with field macros; expand it
		// against the contract to raw WAT. A bare (module …) passes straight through
		// (macro.Expand leaves non-macro WAT untouched).
		src := extractMacroWAT(resp)
		if src == "" {
			return extractWAT(resp), "", nil
		}
		w, cerr := macro.Expand(src, macroFields(layout))
		if cerr != nil {
			return "", src, cerr
		}
		return w, src, nil
	}
	if layout != nil {
		if fluxIsForth() {
			// The model may answer in Forth (the requested surface) OR fall back to the
			// s-expr (cell …) form — Gemini in particular defaults to s-expr for a
			// functional language. ACCEPT EITHER: both lower to the identical IR, so a
			// surface-stubborn model still yields a usable cell instead of failing with
			// `unknown word "(cell"`. Prefer an explicit (cell …) when present.
			if src := extractFlux(resp); src != "" {
				w, cerr := flux.Compile("cell", src, layout)
				if cerr != nil {
					return "", src, cerr
				}
				return w, src, nil
			}
			// The model may also just emit raw WAT (module …); take it directly rather
			// than feeding "(module" into the Forth parser as an unknown word.
			if strings.Contains(resp, "(module") {
				return extractWAT(resp), "", nil
			}
			if src := extractForth(resp); src != "" {
				w, cerr := flux.CompileWith(flux.Forth{}, "cell", src, layout)
				if cerr != nil {
					return "", src, cerr
				}
				return w, src, nil
			}
			return extractWAT(resp), "", nil
		}
		if src := extractFlux(resp); src != "" {
			w, cerr := flux.Compile("cell", src, layout)
			if cerr != nil {
				return "", src, cerr
			}
			return w, src, nil
		}
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

// stripLeadingProse drops a leading run of English prose tokens — ones that begin with
// an ASCII uppercase letter (e.g. "Now", "Let", "The", "Here's", "I"). No valid Flux/
// Forth token starts with A-Z (identifiers are lowercase snake_case, the rest are
// digits, operators, =:, ->, ?, or #x… colors), so this is safe on real programs and
// recovers the common "Now, here is the code: <forth>" pattern a flailing model emits.
func stripLeadingProse(s string) string {
	toks := strings.Fields(s)
	i := 0
	for i < len(toks) && toks[i][0] >= 'A' && toks[i][0] <= 'Z' {
		i++
	}
	if i == 0 {
		return strings.TrimSpace(s) // no leading prose — preserve original spacing
	}
	return strings.Join(toks[i:], " ")
}

// extractForth isolates the Forth word stream from a completion: the contents of a
// fenced code block if present, else the trimmed whole with any <think> block and
// leading English-prose preamble stripped. Unlike (cell …) there is no bracketing form
// to key on, so residual prose is still caught by the checker/repair loop — but a
// flailing model's narration ("Now the cell…") no longer poisons the very first word.
func extractForth(resp string) string {
	s := stripThink(resp)
	if i := strings.Index(s, "```"); i >= 0 {
		rest := s[i+3:]
		if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
			rest = rest[nl+1:]
		}
		if j := strings.Index(rest, "```"); j >= 0 {
			s = rest[:j]
		} else {
			s = rest
		}
		return strings.TrimSpace(s) // fenced content is authoritative — no prose strip
	}
	return stripLeadingProse(s)
}

// extractFlux isolates the outermost balanced (cell …) form from a completion,
// tolerating markdown fences and surrounding prose. Returns "" if none.
func extractFlux(resp string) string {
	s := stripThink(resp)
	if i := strings.Index(s, "```"); i >= 0 {
		rest := s[i+3:]
		if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
			rest = rest[nl+1:]
		}
		if j := strings.Index(rest, "```"); j >= 0 {
			s = rest[:j]
		} else {
			s = rest
		}
	}
	start := strings.Index(s, "(cell")
	if start < 0 {
		return ""
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
				return s[start : i+1]
			}
		}
	}
	return ""
}

// fluxSeedBlock renders the Flux authoring instructions appended to the build
// seed when the Flux path is on: it tells the model to output a (cell …) program
// (not WAT) and gives the language description — the grammar + the typed field
// list — so the model writes only logic and the lowerer owns the encoding. It
// deliberately embeds NO worked Flux program: the worked example is retrieved from
// the knowledge base and lazily inlined by renderKnowledge, so Flux lives in exactly
// one place (the example store) and a language change never has to hunt for Flux
// hard-coded in a prompt. See docs/language-evolution.md §6 and docs/lineage.md §8.
func fluxSeedBlock(contract *EntryContract, layout flux.Layout) string {
	if fluxIsForth() {
		return forthSeedBlock(contract, layout)
	}
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
	b.WriteString("\n=== OUTPUT FORMAT: FLUX (author logic, NOT WAT) ===\n")
	b.WriteString("Write this cell as a Flux functional program — a typed S-expression that is\n")
	b.WriteString("compiled to WASM for you. Do NOT write WAT, WASM, or (module …). Output ONLY a (cell …) form.\n\n")
	b.WriteString("A cell is a PURE FUNCTION over shared state; the compiler owns all memory, stack, and types:\n")
	if view {
		b.WriteString("  (cell NAME (reads <fields>) (draw <prims>))\n")
		b.WriteString("  prims: (circle cx cy r color) (rect x y w h color) (line x1 y1 x2 y2 color); color is #xRRGGBBAA.\n")
	} else {
		b.WriteString("  (cell NAME (reads <fields>) (writes <fields>) BODY)\n")
		b.WriteString("  BODY = optional (let ([name expr]…) …) ending in (write (field expr) …). Unwritten fields keep their value.\n")
	}
	b.WriteString("Expressions: Int/Float/Bool/Color literals (42, 3.14, true, #xFF8800FF); field & let names;\n")
	b.WriteString("(if cond then else); primitives  + - * / mod neg abs min max clamp  < <= > >= = != and or not.\n")
	b.WriteString("Comparisons yield Bool; there is no implicit numeric coercion.\n\n")
	fmt.Fprintf(&b, "SHARED STATE fields (read and write, within your enforced boundary above):\n  %s\n", strings.Join(stateFields, ", "))
	if len(inputFields) > 0 {
		fmt.Fprintf(&b, "INPUT fields (READ-ONLY hardware — the host writes them each tick; NEVER write them):\n  %s\n", strings.Join(inputFields, ", "))
		b.WriteString("  hmi_event_type: 2 mousedown, 3 mouseup, 4 click, 5 keydown, 6 keyup. Detect a NEW discrete event by comparing hmi_event_seq to the value you saw last tick. hmi_key is the key code; hmi_mouse_x/y is the live cursor; hmi_buttons/hmi_modifiers are bit masks.\n")
	}
	b.WriteString("\n")
	b.WriteString("A WORKED FLUX EXAMPLE for this cell's kind is provided above under RELEVANT KNOWLEDGE — follow its shape.\n")
	b.WriteString("Output only your (cell …) program.\n")
	return b.String()
}

// forthSeedBlock is the authoring block when the Forth surface is active: it inlines
// the type-stratified Forth dictionary (the atomic building blocks) plus the cell's
// writable/read-only field lists, and instructs the model to emit only the word
// stream. Same IR, so downstream verification is unchanged.
func forthSeedBlock(contract *EntryContract, layout flux.Layout) string {
	view := contract != nil && contract.Name == "render-frame"
	var stateFields, inputFields []string
	for n, f := range layout {
		if f.ReadOnly {
			inputFields = append(inputFields, n)
		} else {
			stateFields = append(stateFields, n)
		}
	}
	sort.Strings(stateFields)
	sort.Strings(inputFields)

	var b strings.Builder
	b.WriteString("\n=== OUTPUT FORMAT: FORTH (a postfix word stream — NOT WAT, NOT S-expression) ===\n")
	b.WriteString(flux.ForthTypedGuide())
	b.WriteString("\n\n")
	fmt.Fprintf(&b, "SHARED STATE fields you may write with `-> field`:\n  %s\n", strings.Join(stateFields, ", "))
	if len(inputFields) > 0 {
		fmt.Fprintf(&b, "INPUT fields (READ-ONLY hardware — push to read, NEVER write):\n  %s\n", strings.Join(inputFields, ", "))
		b.WriteString("  hmi_event_type: 2 mousedown 3 mouseup 4 click 5 keydown 6 keyup; compare hmi_event_seq to last tick for a NEW event; hmi_key is the key code.\n")
	}
	if view {
		b.WriteString("This is a VIEW cell: emit draws (e.g. `ball_x ball_y 8 #xFFCC33FF circle`). Do not use ->.\n")
	} else {
		b.WriteString("This is a COMPUTE cell: write each updated field with `-> field`.\n")
	}
	b.WriteString("Output ONLY the word stream (a ``` fence is fine). No prose, no (cell …), no WAT.\n")
	return b.String()
}

// runFluxCell lowers a Flux program and runs it once (or `steps` times) in the
// real deterministic sandbox with the given inputs, returning a human-readable
// summary of the resulting writable-field values (compute cells) or drawn
// primitives (view cells). It is the engine of the flux_run agentic tool: it lets
// the authoring model TEST its cell empirically — set inputs, see outputs, iterate
// — instead of guessing. inputsJSON is a JSON object of field name → integer.
func runFluxCell(layout flux.Layout, src, inputsJSON string, steps int) (string, error) {
	wat, err := flux.CompileWith(activeSurface(), "cell", src, layout)
	if err != nil {
		return "", err
	}
	art, aerr := compiler.NewCompilerService().CompileGenotype(wat)
	if aerr != nil || art == nil || !art.SyntaxPassed {
		return "", fmt.Errorf("lowered WAT did not assemble")
	}

	// Seed the inputs the model chose.
	raw := map[string]json.Number{}
	_ = json.Unmarshal([]byte(inputsJSON), &raw)
	var seeds []SeedWrite
	var unknown []string
	for name, num := range raw {
		f, ok := layout[name]
		if !ok {
			unknown = append(unknown, name)
			continue
		}
		iv, _ := num.Int64()
		seeds = append(seeds, SeedWrite{At: fmt.Sprintf("0x%X", f.Offset), U32: []uint32{uint32(int32(iv))}})
	}

	entry := "run-tick"
	if strings.Contains(wat, "render-frame") {
		entry = "render-frame"
	}
	if steps < 1 {
		steps = 1
	}

	// Read back every writable field after the run, in a stable order.
	type wf struct {
		name string
		off  uint32
	}
	var writables []wf
	for name, f := range layout {
		if !f.ReadOnly {
			writables = append(writables, wf{name, f.Offset})
		}
	}
	sort.Slice(writables, func(i, j int) bool { return writables[i].name < writables[j].name })
	// Capture each writable field's value at EVERY tick (the trajectory), not just
	// the final value — so multi-step behavior is visible: a field that reaches a
	// wall and STOPS (clamp) reads differently from one that reverses (bounce), and
	// a frozen cell shows a flat line. This is what lets the model validate the full
	// goal behavior, not just that one tick moved something.
	trajExp := make([]TrajectoryExpect, len(writables))
	for i, w := range writables {
		trajExp[i] = TrajectoryExpect{At: fmt.Sprintf("0x%X", w.off)}
	}

	sc := Scenario{Entry: entry, Steps: steps, Seed: seeds, Expect: ScenarioExpect{Trajectory: trajExp}}
	_, frames, _, _, traj, rerr := execScenario(context.Background(), art.Bytecode, sc, DefaultPayloadOffset, DefaultStateWindow, nil)
	if rerr != nil {
		return "", rerr
	}

	var b strings.Builder
	if len(unknown) > 0 {
		fmt.Fprintf(&b, "(ignored unknown input field(s): %s)\n", strings.Join(unknown, ", "))
	}
	if entry == "render-frame" {
		var last []DrawRecord
		if len(frames) > 0 {
			last = frames[len(frames)-1]
		}
		if len(last) == 0 {
			b.WriteString("drew nothing")
			return b.String(), nil
		}
		name := map[int32]string{1: "rect", 2: "line", 3: "circle"}
		for _, r := range last {
			fmt.Fprintf(&b, "drew %s a=%d b=%d c=%d d=%d rgba=0x%08X\n", name[r.Op], r.A, r.B, r.C, r.D, r.RGBA)
		}
		return strings.TrimRight(b.String(), "\n"), nil
	}
	fmt.Fprintf(&b, "trajectory over %d tick(s) — each field's value per tick (watch for clamp/stop vs reverse/bounce, and freezes):\n", steps)
	lines := make([]string, len(writables))
	for i, w := range writables {
		var seq []uint32
		if i < len(traj) {
			seq = traj[i]
		}
		lines[i] = fmt.Sprintf("  %s: %s", w.name, sampleSeq(seq))
	}
	b.WriteString(strings.Join(lines, "\n"))
	return b.String(), nil
}

// sampleSeq renders a per-tick value sequence compactly: in full when short,
// head…tail when long, so a long run stays readable while the shape is visible.
func sampleSeq(seq []uint32) string {
	toStr := func(sub []uint32) []string {
		out := make([]string, len(sub))
		for i, v := range sub {
			out[i] = fmt.Sprintf("%d", int32(v))
		}
		return out
	}
	if len(seq) <= 16 {
		return "[" + strings.Join(toStr(seq), ",") + "]"
	}
	return "[" + strings.Join(toStr(seq[:7]), ",") + ",…," + strings.Join(toStr(seq[len(seq)-7:]), ",") + "]"
}

// fluxCorrectionDirective renders the sieve repair prompt for a Flux compile
// error — a semantic sentence (a type/shape/parse message), not a stack trace.
func fluxCorrectionDirective(err error) string {
	form := "(cell …) program"
	if fluxIsForth() {
		form = "Forth word stream"
	}
	return fmt.Sprintf(`[INNER SIEVE CORRECTION DIRECTIVE]
Your %s did not compile.
DEFECT: %v
Regenerate the complete %s, fixing exactly that (watch the stack effects; bind reused/deep values with =:). Output only the program.`, form, err, form)
}

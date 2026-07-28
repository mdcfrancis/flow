package evolution

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/mdcfrancis/flow/compiler"
	"github.com/mdcfrancis/flow/flux"
	"github.com/mdcfrancis/flow/inference"
	"github.com/mdcfrancis/flow/storage"
)

// agenticMaxSteps bounds the tool-calling loop so a cell can never spin forever.
const agenticMaxSteps = 8

// ToolReasoner is a model that supports the client-side agentic tool loop.
// inference.LocalModelClient satisfies it.
type ToolReasoner interface {
	InvokeTools(ctx context.Context, systemPrompt, userContext string, tools []inference.ToolDef, exec inference.ToolExec, maxSteps int) (string, error)
}

const agenticPreamble = `You have TOOLS. Use them before you finalize:
- find_docs / find_examples: retrieve the how-to pattern and a WORKED WAT example for what you are building.
- inspect_example / read_doc: fetch the full text of a specific one by id.
- compile_check: assemble a draft WAT through the real HDM assembler and get the exact error back. ALWAYS compile_check your candidate and fix any error before answering.
When finished, reply with ONLY the final complete (module ...) form — no tool call, no prose.

`

// watDirectPreamble drives a TOOL-LESS backend to author a raw WAT module in one shot
// (used only for a layout-less system cell; a cell with a contract layout authors
// macro-WAT via macroPreamble).
const watDirectPreamble = `Author the cell and reply with ONLY the complete, correct WAT (module …) — no prose, no tool calls.`

// authorWithCorrection drives a TOOL-LESS backend (e.g. Gemini) by re-prompting with
// the compile error until the program compiles or attempts run out — the no-tool
// analogue of the agentic loop, so a model that cannot call flux_check still self-
// corrects its near-misses. Returns a compiling response as soon as one is produced,
// else the last attempt (the caller's verification then reports its error).
func authorWithCorrection(ctx context.Context, model ToolReasoner, cs *compiler.CompilerService, systemPrompt, seed string, layout flux.Layout, prologue string, maxSteps int) (string, error) {
	if maxSteps < 1 {
		maxSteps = 1
	}
	user, last := seed, ""
	for i := 0; i < maxSteps; i++ {
		resp, err := model.InvokeTools(ctx, systemPrompt, user, nil, nil, 1) // nil tools ⇒ plain completion
		if err != nil {
			return last, err
		}
		last = resp
		wat, _, ferr := candidateWAT(resp, layout, prologue)
		if ferr == nil {
			if art, cerr := cs.CompileGenotype(wat); cerr == nil && art != nil && art.SyntaxPassed {
				return resp, nil // compiles cleanly — done
			} else if cerr != nil {
				ferr = cerr
			} else if art != nil {
				ferr = fmt.Errorf("%s (line %d)", art.ErrorContext, art.ErrorLine)
			} else {
				ferr = fmt.Errorf("empty program")
			}
		}
		user = seed + "\n\nYOUR PREVIOUS ANSWER FAILED TO COMPILE:\n" + resp +
			"\n\nEXACT ERROR: " + ferr.Error() +
			"\nFix ONLY that error and reply with the complete corrected program — nothing else."
	}
	return last, nil
}

// macroPreamble teaches the MACRO-WAT surface (the operational default): the model
// writes native WebAssembly text, but names shared-state fields and skips the module
// boilerplate via a handful of macros. Native f32 — use it for continuous quantities
// (position, velocity, force) to avoid integer-division underflow.
const macroPreamble = `You write a cell in WAT (WebAssembly text) using these MACROS — do not hand-write the module, the memory import, or field offsets:

  (cell run-tick BODY…)                  a COMPUTE cell. BODY ends with (i32.const 0).
  (cell render-frame BODY…)              a UI cell. BODY draws with (draw …)/(scene …);
                                         the cell returns the drawn byte length for you.
  (get NAME)        read shared field NAME  (f32 if the field is f32, else i32)
  (set NAME EXPR)   write EXPR to field NAME (store type matches the field)
  (geti NAME)       field NAME as an i32 — TRUNCATES an f32 field, for pixel coords
  (atidx NAME IDX)  / (setidx NAME IDX EXPR)   array element read / write (IDX may be a $loop var)
  (field NAME)      the raw i32 base offset of NAME

DRAWING (render-frame cells):
  (draw PRIM)       append ONE primitive to the frame. PRIM = (circle X Y R COLOR) |
                    (rect X Y W H COLOR) | (line X1 Y1 X2 Y2 COLOR); COLOR = (i32.const 0xRRGGBBAA).
  (scene PRIM…)     shorthand for several (draw …) in a row (a fixed set of shapes).
  To draw a VARIABLE number of things (one per element of an ARRAY field), LOOP and draw:
      (for $i (get count) (draw (circle (atidx px $i) (atidx py $i) (i32.const 3) COLOR)))
  A field typed i32[N] is an ARRAY — read element i with (atidx name $i). Draw a scalar
  (single) thing with one (draw …); draw an array of them with (for … (draw …)).

ITERATION:
  (for $i COUNT BODY…)   run BODY for $i = 0,1,…,COUNT-1. Use it to update every element of
                         an array ((setidx …)) or draw one primitive per element. Nest for a grid.

Everything else is ordinary WAT: i32.add/sub/mul/div, f32.add/sub/mul/div, f32.const 1.5,
i32.trunc_f32_s, (local $t f32), (local.set $t …)/(local.get $t), etc. Locals may be declared
anywhere — they are hoisted for you.

USE FLOATS for continuous physics: if a field is f32, (get it) loads f32 and you do f32.*
math — so 500000.0 / dist does NOT floor to zero the way integer division does. Convert
to int only at the edges (geti for draw coords).

Your typed fields, the cell's role, and a worked example are in the task below. Reply with
ONLY the complete (cell …) program — no prose, no explanation, no markdown fence.

`

// RunAgenticSieve synthesizes a cell with the CLIENT-SIDE agentic loop: the model may
// call knowledge-base + compiler tools (retrieve a worked example, read a how-to,
// compile-check a draft) while it works. The final WAT is extracted, assembled, and
// entry-checked exactly like RunSieve, so its outcome plugs into the same verification
// gates unchanged — the tools inform synthesis, they never bypass verification.
func RunAgenticSieve(ctx context.Context, model ToolReasoner, ledger *storage.LedgerEngine, systemPrompt, seedContext, kind, intent string, layout flux.Layout, prologue string, contract *EntryContract) (*SieveOutcome, error) {
	cs := compiler.NewCompilerService()
	tools, exec := buildAgenticTools(ledger, cs, kind, intent, layout)
	// A backend WITHOUT tool support (Gemini) must get a DIRECT preamble — a tool-USING
	// preamble makes it try to call an undeclared function (MALFORMED_FUNCTION_CALL) and
	// return empty. Detected via an optional interface so mocks/other reasoners are
	// unaffected (default: has tools).
	hasTools := true
	if st, ok := model.(interface{ SupportsTools() bool }); ok {
		hasTools = st.SupportsTools()
	}
	// A cell with a contract layout authors MACRO-WAT (the operational surface); a
	// layout-less system cell authors raw WAT.
	macroMode := layout != nil
	var preamble string
	switch {
	case macroMode:
		preamble = macroPreamble
	case !hasTools:
		preamble = watDirectPreamble
	default:
		preamble = agenticPreamble
	}
	var resp string
	var err error
	if hasTools && !macroMode {
		resp, err = model.InvokeTools(ctx, preamble+systemPrompt, seedContext, tools, exec, agenticMaxSteps)
	} else {
		// Macro-WAT and tool-less backends both author in ONE shot. Drive a RE-PROMPT
		// correction loop: feed the exact expand/compile error back and ask for a
		// corrected program, up to agenticMaxSteps times.
		resp, err = authorWithCorrection(ctx, model, cs, preamble+systemPrompt, seedContext, layout, prologue, agenticMaxSteps)
	}
	if err != nil {
		return nil, fmt.Errorf("agentic sieve: %w", err)
	}
	// With a layout the model answered with macro-WAT (a (cell …) form), expanded to
	// raw WAT here — the same path as the standard sieve.
	wat, fluxSrc, ferr := candidateWAT(resp, layout, prologue)
	if ferr != nil {
		taxoWAT(fluxSrc, nil, ferr.Error())
		return &SieveOutcome{WAT: fluxSrc, Raw: resp},
			fmt.Errorf("agentic sieve: macro-WAT did not expand: %v", ferr)
	}
	if fluxSrc != "" {
		log.Printf("[FLUX] agentic: expanded model-authored macro-WAT to raw WAT")
	}
	art, cerr := cs.CompileGenotype(wat)
	if cerr != nil || art == nil || !art.SyntaxPassed {
		taxoWAT(wat, art, "") // agentic final WAT — folded into the compile-stage buckets
		line, msg := 0, "unknown"
		if art != nil {
			line, msg = art.ErrorLine, art.ErrorContext
		}
		return &SieveOutcome{Artifact: art, WAT: wat, Raw: resp},
			fmt.Errorf("agentic sieve: final WAT did not compile: %s (line %d)", msg, line)
	}
	if contract != nil {
		if sigErr := checkEntrySignature(ctx, art.Bytecode, contract); sigErr != nil {
			taxoWAT(wat, art, sigErr.Error())
			return &SieveOutcome{Artifact: art, WAT: wat, Raw: resp},
				fmt.Errorf("agentic sieve: entry contract unmet: %v", sigErr)
		}
	}
	taxoWAT(wat, art, "")
	return &SieveOutcome{Artifact: art, WAT: wat, Flux: fluxSrc, Iterations: 1, Raw: resp}, nil
}

// buildAgenticTools returns the tool definitions and a Go executor bound to the
// knowledge base + compiler. Every tool is read-only except that compile_check runs the
// assembler (no side effects); the model's produced WAT is still fully verified later.
func buildAgenticTools(ledger *storage.LedgerEngine, cs *compiler.CompilerService, kind, intent string, layout flux.Layout) ([]inference.ToolDef, inference.ToolExec) {
	defs := []inference.ToolDef{
		{Name: "list_examples", Description: "List available worked WAT examples (id + one-line semantics) for this cell's kind.", Parameters: objSchema(nil, nil)},
		{Name: "find_examples", Description: "Search worked WAT examples by a query; returns the best matches with their WAT.", Parameters: objSchema(map[string]string{"query": "what you want a worked example of"}, []string{"query"})},
		{Name: "inspect_example", Description: "Return the full WAT of one example by id.", Parameters: objSchema(map[string]string{"id": "the example id"}, []string{"id"})},
		{Name: "find_docs", Description: "Search how-to documents on architectural patterns; returns titles + bodies.", Parameters: objSchema(map[string]string{"query": "the topic to look up"}, []string{"query"})},
		{Name: "read_doc", Description: "Return the full body of one document by id.", Parameters: objSchema(map[string]string{"id": "the document id"}, []string{"id"})},
		{Name: "compile_check", Description: "Assemble a WAT (module ...) through the HDM assembler; returns 'ok' or the exact error. Use before finalizing.", Parameters: objSchema(map[string]string{"wat": "the full WAT module source"}, []string{"wat"})},
	}
	const toolLang = "wat"
	exec := func(name, argsJSON string) string {
		args := map[string]any{}
		_ = json.Unmarshal([]byte(argsJSON), &args)
		getStr := func(k string) string {
			if v, ok := args[k].(string); ok {
				return v
			}
			return ""
		}
		log.Printf("[TOOL] %s(%s)", name, truncate(argsJSON, 100))
		switch name {
		case "list_examples":
			var b strings.Builder
			for _, e := range FindExamples(ledger, kind, toolLang, "", nil, nil, 20) {
				fmt.Fprintf(&b, "%s: %s\n", e.ID, e.Semantics)
			}
			return orNone(b.String(), "no examples stored")
		case "find_examples":
			q := getStr("query")
			if q == "" {
				q = intent
			}
			var b strings.Builder
			for _, e := range FindExamples(ledger, kind, toolLang, q, nil, nil, 3) {
				fmt.Fprintf(&b, "EXAMPLE (%s, score %s):\n%s\n\n", e.Semantics, e.Score, e.WAT)
			}
			return orNone(b.String(), "no matching examples")
		case "inspect_example":
			id := getStr("id")
			for _, e := range LoadExamples(ledger) {
				if e.ID == id {
					return e.WAT
				}
			}
			return "no example with that id"
		case "find_docs":
			q := getStr("query")
			if q == "" {
				q = intent
			}
			var b strings.Builder
			for _, d := range FindDocuments(ledger, kind, q, 3) {
				fmt.Fprintf(&b, "%s (%s) — %s\n\n", d.Title, d.ID, d.Body)
			}
			return orNone(b.String(), "no matching documents")
		case "read_doc":
			id := getStr("id")
			for _, d := range LoadDocuments(ledger) {
				if d.ID == id {
					return d.Body
				}
			}
			return "no document with that id"
		case "compile_check":
			src := getStr("wat")
			art, err := cs.CompileGenotype(src)
			if err != nil || art == nil || !art.SyntaxPassed {
				line, msg := 0, "unknown"
				if art != nil {
					line, msg = art.ErrorLine, art.ErrorContext
				}
				if err != nil {
					msg = err.Error()
				}
				return fmt.Sprintf("COMPILE ERROR (line %d): %s", line, msg)
			}
			return "ok: assembles cleanly"
		}
		return "unknown tool: " + name
	}
	return defs, exec
}

// objSchema builds a JSON-Schema object for a tool's string parameters.
func objSchema(props map[string]string, required []string) map[string]any {
	p := map[string]any{}
	for name, desc := range props {
		p[name] = map[string]any{"type": "string", "description": desc}
	}
	s := map[string]any{"type": "object", "properties": p}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func orNone(s, none string) string {
	if strings.TrimSpace(s) == "" {
		return none
	}
	return s
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

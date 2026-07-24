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

// fluxAgenticPreamble drives TEST-DRIVEN Flux authoring: the model writes a
// (cell …) program, checks it compiles, and — the key step — RUNS it in the real
// sandbox on inputs it chooses, reads the outputs, and iterates until the behavior
// matches the goal. This turns synthesis from "guess the syntax" into an empirical
// loop, which is what lets a model that has the logic but not the grammar converge.
const fluxAgenticPreamble = `You author a cell in FLUX — a small typed functional language (its grammar, your exact typed fields, and a worked example are in the task below). You have TOOLS; USE them and ITERATE until the cell is correct — do not answer on the first draft.
- find_docs / find_examples / read_doc: retrieve a relevant how-to or worked pattern.
- flux_check(src): parse + type-check + lower your (cell …). It returns "ok" or the exact error (an unknown field, a type mismatch, a syntax slip). Fix EVERY error before running.
- flux_run(src, inputs): RUN your cell in the real sandbox with inputs YOU choose and read the resulting field values (or drawn shapes) back. This is how you VERIFY behavior: pick inputs that exercise the goal AND the edge cases the acceptance checks describe (e.g. a value at a wall, a specific key), run, and confirm the outputs are exactly what the goal requires. If an output is wrong, fix the LOGIC and run again.
Only after flux_run shows the correct behavior, reply with ONLY the final complete (cell …) program — no tool call, no prose, no WAT.

`

// RunAgenticSieve synthesizes a cell with the CLIENT-SIDE agentic loop: the model may
// call knowledge-base + compiler tools (retrieve a worked example, read a how-to,
// compile-check a draft) while it works. The final WAT is extracted, assembled, and
// entry-checked exactly like RunSieve, so its outcome plugs into the same verification
// gates unchanged — the tools inform synthesis, they never bypass verification.
func RunAgenticSieve(ctx context.Context, model ToolReasoner, ledger *storage.LedgerEngine, systemPrompt, seedContext, kind, intent string, layout flux.Layout, contract *EntryContract) (*SieveOutcome, error) {
	cs := compiler.NewCompilerService()
	tools, exec := buildAgenticTools(ledger, cs, kind, intent, layout)
	preamble := agenticPreamble
	if layout != nil {
		preamble = fluxAgenticPreamble
	}
	resp, err := model.InvokeTools(ctx, preamble+systemPrompt, seedContext, tools, exec, agenticMaxSteps)
	if err != nil {
		return nil, fmt.Errorf("agentic sieve: %w", err)
	}
	// Flux-aware: with a layout the model may answer with a (cell …) program, which
	// is lowered to WAT here — the same path as the standard sieve.
	wat, fluxSrc, ferr := candidateWAT(resp, layout)
	if ferr != nil {
		taxoWAT(fluxSrc, nil, ferr.Error())
		return &SieveOutcome{WAT: fluxSrc, Raw: resp},
			fmt.Errorf("agentic sieve: flux did not compile: %v", ferr)
	}
	if fluxSrc != "" {
		log.Printf("[FLUX] agentic: lowered a model-authored (cell …) to WAT")
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
	return &SieveOutcome{Artifact: art, WAT: wat, Iterations: 1, Raw: resp}, nil
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
	// In Flux mode, add the test-driven authoring tools: check a (cell …) program
	// and RUN it in the real sandbox on chosen inputs to verify behavior.
	if layout != nil {
		defs = append(defs,
			inference.ToolDef{Name: "flux_check", Description: "Parse, type-check, and lower a Flux (cell …) program; returns 'ok' or the exact error (unknown field, type mismatch, syntax). Use before flux_run.", Parameters: objSchema(map[string]string{"src": "the full (cell …) Flux program"}, []string{"src"})},
			inference.ToolDef{Name: "flux_run", Description: "Run your Flux (cell …) in the real sandbox with inputs you choose, and get the resulting field values (or drawn shapes) back — verify behavior empirically before answering. inputs is a JSON object of field name to integer.", Parameters: objSchema(map[string]string{"src": "the full (cell …) Flux program", "inputs": "JSON object mapping field names to integers, e.g. {\"player_x\":100,\"hmi_key\":39}"}, []string{"src", "inputs"})},
		)
	}
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
			for _, e := range FindExamples(ledger, kind, "", nil, nil, 20) {
				fmt.Fprintf(&b, "%s: %s\n", e.ID, e.Semantics)
			}
			return orNone(b.String(), "no examples stored")
		case "find_examples":
			q := getStr("query")
			if q == "" {
				q = intent
			}
			var b strings.Builder
			for _, e := range FindExamples(ledger, kind, q, nil, nil, 3) {
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
			// Flux-aware: if the model checks a (cell …) program, lower it first so the
			// error it gets back is the Flux (semantic) error, not "expected module".
			if layout != nil {
				if f := extractFlux(src); f != "" {
					w, lerr := flux.Compile("cell", f, layout)
					if lerr != nil {
						return "FLUX COMPILE ERROR: " + lerr.Error()
					}
					src = w
				}
			}
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
		case "flux_check":
			if layout == nil {
				return "flux_check is unavailable for this cell"
			}
			if _, err := flux.Compile("cell", getStr("src"), layout); err != nil {
				return "FLUX ERROR: " + err.Error()
			}
			return "ok: parses, type-checks, and lowers to WASM"
		case "flux_run":
			if layout == nil {
				return "flux_run is unavailable for this cell"
			}
			out, err := runFluxCell(layout, getStr("src"), getStr("inputs"), 1)
			if err != nil {
				return "RUN ERROR: " + err.Error()
			}
			return out
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

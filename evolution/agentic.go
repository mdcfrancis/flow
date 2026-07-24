package evolution

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/mdcfrancis/flow/compiler"
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

// RunAgenticSieve synthesizes a cell with the CLIENT-SIDE agentic loop: the model may
// call knowledge-base + compiler tools (retrieve a worked example, read a how-to,
// compile-check a draft) while it works. The final WAT is extracted, assembled, and
// entry-checked exactly like RunSieve, so its outcome plugs into the same verification
// gates unchanged — the tools inform synthesis, they never bypass verification.
func RunAgenticSieve(ctx context.Context, model ToolReasoner, ledger *storage.LedgerEngine, systemPrompt, seedContext, kind, intent string, contract *EntryContract) (*SieveOutcome, error) {
	cs := compiler.NewCompilerService()
	tools, exec := buildAgenticTools(ledger, cs, kind, intent)
	resp, err := model.InvokeTools(ctx, agenticPreamble+systemPrompt, seedContext, tools, exec, agenticMaxSteps)
	if err != nil {
		return nil, fmt.Errorf("agentic sieve: %w", err)
	}
	wat := extractWAT(resp)
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
func buildAgenticTools(ledger *storage.LedgerEngine, cs *compiler.CompilerService, kind, intent string) ([]inference.ToolDef, inference.ToolExec) {
	defs := []inference.ToolDef{
		{Name: "list_examples", Description: "List available worked WAT examples (id + one-line semantics) for this cell's kind.", Parameters: objSchema(nil, nil)},
		{Name: "find_examples", Description: "Search worked WAT examples by a query; returns the best matches with their WAT.", Parameters: objSchema(map[string]string{"query": "what you want a worked example of"}, []string{"query"})},
		{Name: "inspect_example", Description: "Return the full WAT of one example by id.", Parameters: objSchema(map[string]string{"id": "the example id"}, []string{"id"})},
		{Name: "find_docs", Description: "Search how-to documents on architectural patterns; returns titles + bodies.", Parameters: objSchema(map[string]string{"query": "the topic to look up"}, []string{"query"})},
		{Name: "read_doc", Description: "Return the full body of one document by id.", Parameters: objSchema(map[string]string{"id": "the document id"}, []string{"id"})},
		{Name: "compile_check", Description: "Assemble a WAT (module ...) through the HDM assembler; returns 'ok' or the exact error. Use before finalizing.", Parameters: objSchema(map[string]string{"wat": "the full WAT module source"}, []string{"wat"})},
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
			art, err := cs.CompileGenotype(getStr("wat"))
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

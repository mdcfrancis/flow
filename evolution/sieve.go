package evolution

import (
	"context"
	"fmt"
	"strings"

	"github.com/mdcfrancis/flow/compiler"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

// Reasoner is the cognitive-engine boundary the sieve drives. inference.
// LocalModelClient satisfies it; tests supply deterministic fakes.
type Reasoner interface {
	InvokeReasoning(ctx context.Context, systemPrompt, userContext string) (string, error)
}

// SieveOutcome reports the result of the inner recursive synthesis loop.
type SieveOutcome struct {
	Artifact   *compiler.CompilationArtifact
	WAT        string
	Iterations int
	Raw        string // the model's full final response (for structural NEW-PRIMITIVE extraction)
}

// EntryContract is the exact signature a synthesized cell's entry export must
// satisfy. The sieve enforces it inside the repair loop, so a misnamed or
// wrong-arity entry (a common model mistake, e.g. a 4-param run-tick) is caught
// and fed back for repair instead of trapping later in the gauntlet.
type EntryContract struct {
	Name    string
	Params  []api.ValueType
	Results []api.ValueType
}

// Standard cell entry contracts (both are (i32,i32)->i32, differing by export
// name — the check rejects a wrong name too).
var (
	RunTickContract     = &EntryContract{Name: EntryPoint, Params: []api.ValueType{api.ValueTypeI32, api.ValueTypeI32}, Results: []api.ValueType{api.ValueTypeI32}}
	RenderFrameContract = &EntryContract{Name: "render-frame", Params: []api.ValueType{api.ValueTypeI32, api.ValueTypeI32}, Results: []api.ValueType{api.ValueTypeI32}}
)

// entryContractFor returns the contract matching a cell's exported entry: the
// render-frame contract for UI cells, run-tick otherwise. The evolution loop is
// thus entry-agnostic — a cell is evolved against whichever monadic entry it
// implements.
func entryContractFor(genotype string) *EntryContract {
	if strings.Contains(genotype, `"render-frame"`) {
		return RenderFrameContract
	}
	return RunTickContract
}

// RunSieve is the inner loop of the dual-loop structural synthesis: it asks the
// model for a WAT genotype, assembles it through the native compiler, and — if
// assembly fails — feeds a precise line/context correction directive back into
// the model, looping up to maxIters times. It returns the first artifact that
// clears the syntax sieve, or an error carrying the final diagnostic.
// The optional contract, when supplied, additionally requires the compiled
// candidate to export the named entry with the exact signature; a mismatch is
// treated like a compile failure and fed back to the model for repair.
func RunSieve(ctx context.Context, model Reasoner, systemPrompt, seedContext string, maxIters int, contract ...*EntryContract) (*SieveOutcome, error) {
	if maxIters < 1 {
		maxIters = 1
	}
	var want *EntryContract
	if len(contract) > 0 {
		want = contract[0]
	}
	cs := compiler.NewCompilerService()
	payload := seedContext
	var last *compiler.CompilationArtifact
	var lastSigErr error

	for i := 1; i <= maxIters; i++ {
		resp, err := model.InvokeReasoning(ctx, systemPrompt, payload)
		if err != nil {
			return nil, fmt.Errorf("sieve iteration %d: reasoning invocation failed: %w", i, err)
		}
		wat := extractWAT(resp)
		art, cerr := cs.CompileGenotype(wat)
		last = art
		if cerr == nil && art != nil && art.SyntaxPassed {
			// Syntax cleared; now enforce the entry contract if one was given.
			if want == nil {
				taxoWAT(wat, art, "")
				return &SieveOutcome{Artifact: art, WAT: wat, Iterations: i, Raw: resp}, nil
			}
			if sigErr := checkEntrySignature(ctx, art.Bytecode, want); sigErr != nil {
				taxoWAT(wat, art, sigErr.Error()) // assembled, but type/stack/signature invalid
				lastSigErr = sigErr
				payload = signatureCorrectionDirective(want, sigErr)
				continue
			}
			taxoWAT(wat, art, "")
			return &SieveOutcome{Artifact: art, WAT: wat, Iterations: i, Raw: resp}, nil
		}
		taxoWAT(wat, art, "") // syntax fault — classified from the assembler artifact
		payload = correctionDirective(art)
	}

	if lastSigErr != nil {
		return &SieveOutcome{Artifact: last, Iterations: maxIters},
			fmt.Errorf("sieve did not converge within %d iterations: entry contract unmet: %v", maxIters, lastSigErr)
	}
	line, msg := 0, "unknown"
	if last != nil {
		line, msg = last.ErrorLine, last.ErrorContext
	}
	return &SieveOutcome{Artifact: last, Iterations: maxIters},
		fmt.Errorf("sieve did not converge within %d iterations: %s (line %d)", maxIters, msg, line)
}

// checkEntrySignature compiles the candidate (compile-only, no instantiation, so
// imports need not be satisfied) and verifies its exported entry matches the
// contract's name and signature exactly.
func checkEntrySignature(ctx context.Context, bytecode []byte, c *EntryContract) error {
	r := wazero.NewRuntime(ctx)
	defer r.Close(ctx)
	cm, err := r.CompileModule(ctx, bytecode)
	if err != nil {
		return fmt.Errorf("compile for signature check: %w", err)
	}
	fn, ok := cm.ExportedFunctions()[c.Name]
	if !ok {
		return fmt.Errorf("no exported function named %q", c.Name)
	}
	if !sameTypes(fn.ParamTypes(), c.Params) {
		return fmt.Errorf("%q has params (%s), want (%s)", c.Name, joinTypes(fn.ParamTypes()), joinTypes(c.Params))
	}
	if !sameTypes(fn.ResultTypes(), c.Results) {
		return fmt.Errorf("%q has results (%s), want (%s)", c.Name, joinTypes(fn.ResultTypes()), joinTypes(c.Results))
	}
	return nil
}

// signatureCorrectionDirective renders the repair prompt for an entry-contract
// violation, so the model regenerates with the correct signature.
func signatureCorrectionDirective(c *EntryContract, sigErr error) string {
	return fmt.Sprintf(`[INNER SIEVE CORRECTION DIRECTIVE]
CRITICAL: Your last WAT compiled but VIOLATES THE ENTRY CONTRACT.
DEFECT: %v
REQUIRED: export exactly one function named "%s" with signature (param %s) (result %s).
Do NOT add, remove, rename, or reorder parameters, results, or the export name.
REPAIR ACTIONS: Regenerate the complete WAT module with this exact entry signature. Output only raw code.`,
		sigErr, c.Name, joinTypes(c.Params), joinTypes(c.Results))
}

func sameTypes(a, b []api.ValueType) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func joinTypes(ts []api.ValueType) string {
	if len(ts) == 0 {
		return ""
	}
	names := make([]string, len(ts))
	for i, t := range ts {
		names[i] = typeName(t)
	}
	return strings.Join(names, " ")
}

func typeName(t api.ValueType) string {
	switch t {
	case api.ValueTypeI32:
		return "i32"
	case api.ValueTypeI64:
		return "i64"
	case api.ValueTypeF32:
		return "f32"
	case api.ValueTypeF64:
		return "f64"
	default:
		return fmt.Sprintf("0x%x", t)
	}
}

// correctionDirective renders the inner sieve feedback prompt from
// a failed compilation artifact.
func correctionDirective(art *compiler.CompilationArtifact) string {
	line, ctxMsg := 0, "unspecified parse fault"
	if art != nil {
		line, ctxMsg = art.ErrorLine, art.ErrorContext
	}
	return fmt.Sprintf(`[INNER SIEVE CORRECTION DIRECTIVE]
CRITICAL: Your last generated WAT string failed hard parsing invariants.
LINE DEFECT: %d
CONTEXT EXCEPTION: %s
REPAIR ACTIONS: Regenerate the complete WAT module structure. Fix this token sequence immediately. Output only raw code.`, line, ctxMsg)
}

// extractAllWAT returns every top-level balanced (module ...) form in a model
// completion, in order — used when a pass expects several cells (e.g. fission).
func extractAllWAT(resp string) []string {
	var out []string
	s := resp
	for {
		start := strings.Index(s, "(module")
		if start < 0 {
			return out
		}
		depth, inStr, end := 0, false, -1
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
					end = i
				}
			}
			if end >= 0 {
				break
			}
		}
		if end < 0 {
			out = append(out, strings.TrimSpace(s[start:]))
			return out
		}
		out = append(out, s[start:end+1])
		s = s[end+1:]
	}
}

// extractWAT pulls a WAT module out of a model completion, tolerating markdown
// code fences and surrounding prose by isolating the outermost balanced
// (module ...) form.
func extractWAT(resp string) string {
	s := resp
	// Strip a fenced code block if present.
	if i := strings.Index(s, "```"); i >= 0 {
		rest := s[i+3:]
		// Drop an optional language tag on the fence line.
		if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
			rest = rest[nl+1:]
		}
		if j := strings.Index(rest, "```"); j >= 0 {
			s = rest[:j]
		} else {
			s = rest
		}
	}
	// Isolate the outermost balanced (module ...) expression.
	start := strings.Index(s, "(module")
	if start < 0 {
		return strings.TrimSpace(s)
	}
	depth := 0
	inStr := false
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
	return strings.TrimSpace(s[start:])
}

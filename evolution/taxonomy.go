package evolution

import (
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/mdcfrancis/flow/compiler"
)

// Synthesis-failure taxonomy — a gated measurement probe (HDM_TAXONOMY=1).
//
// It tests the WAT-codegen hypothesis: are synthesis failures dominated by
// DETERMINISTIC BOILERPLATE (compile stage — the assembler role: type widths,
// stack/signature discipline, module structure, memory addressing — which a
// typed IR + lowerer could remove entirely), or by GENUINE LOGIC (acceptance
// stage — the cell compiles but behaves wrong, which only better reasoning
// fixes)?
//
// Every WAT the model emits is classified at the compile stage; every cell that
// clears the sieve is classified at the acceptance stage. Off by default and
// zero-cost; when on it emits a cumulative one-line [TAXONOMY] snapshot so the
// final line of a grow log is the full distribution.
var (
	taxoOn    = os.Getenv("HDM_TAXONOMY") != ""
	taxoMu    sync.Mutex
	taxoStats = map[string]int{}
)

// taxoRecord bumps a bucket and logs the running distribution. key is
// "stage:class", e.g. "wat:type" or "accept:stall".
func taxoRecord(key string) {
	if !taxoOn {
		return
	}
	taxoMu.Lock()
	taxoStats[key]++
	snap := taxoSnapshot()
	taxoMu.Unlock()
	log.Printf("[TAXONOMY] %s", snap)
}

// taxoSnapshot renders the current tallies grouped by stage, compile stage first.
func taxoSnapshot() string {
	keys := make([]string, 0, len(taxoStats))
	for k := range taxoStats {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var wat, acc []string
	for _, k := range keys {
		entry := fmt.Sprintf("%s:%d", strings.TrimPrefix(strings.TrimPrefix(k, "wat:"), "accept:"), taxoStats[k])
		if strings.HasPrefix(k, "wat:") {
			wat = append(wat, entry)
		} else {
			acc = append(acc, entry)
		}
	}
	return fmt.Sprintf("compile{%s} accept{%s}", strings.Join(wat, " "), strings.Join(acc, " "))
}

// classifyWAT buckets one WAT generation. wat is the extracted source; art is the
// assembler artifact (nil/!SyntaxPassed on a syntax fault); errMsg is a
// downstream validator error (e.g. the wazero signature check), which is where
// TYPE and STACK faults surface because the hand-written assembler is lenient.
// "ok" means it assembled AND passed the entry-signature check.
func classifyWAT(wat string, art *compiler.CompilationArtifact, errMsg string) string {
	if strings.TrimSpace(wat) == "" || !strings.Contains(wat, "(module") {
		return "empty" // the model returned prose / no code
	}
	msg := strings.ToLower(errMsg)
	if msg == "" && art != nil {
		msg = strings.ToLower(art.ErrorContext)
	}
	switch {
	case msg == "":
		return "ok"
	case strings.Contains(msg, "i64") || strings.Contains(msg, "type mismatch") || strings.Contains(msg, "expected i32") || strings.Contains(msg, "f32") || strings.Contains(msg, "f64"):
		return "type" // width/type discipline
	case strings.Contains(msg, "result") || strings.Contains(msg, "operand") || strings.Contains(msg, "pop") || strings.Contains(msg, "stack") || strings.Contains(msg, "param") || strings.Contains(msg, "signature") || strings.Contains(msg, "arity"):
		return "stack-sig" // stack discipline / entry signature
	case strings.Contains(msg, "memory") || strings.Contains(msg, "offset") || strings.Contains(msg, "align") || strings.Contains(msg, "out of bounds") || strings.Contains(msg, "address"):
		return "addressing" // shared-memory offset / load-store arithmetic
	case strings.Contains(msg, "table") || strings.Contains(msg, "local") || strings.Contains(msg, "paren") || strings.Contains(msg, "unexpected") || strings.Contains(msg, "unbalanced") || strings.Contains(msg, "expected") || strings.Contains(msg, "token") || strings.Contains(msg, "import") || strings.Contains(msg, "export"):
		return "structural" // module structure / parse invariants
	default:
		return "other"
	}
}

// taxoWAT records one compile-stage outcome for a WAT generation.
func taxoWAT(wat string, art *compiler.CompilationArtifact, errMsg string) {
	if !taxoOn {
		return
	}
	taxoRecord("wat:" + classifyWAT(wat, art, errMsg))
}

// taxoAccept records one acceptance-stage (behavior) outcome for a cell that
// already cleared the sieve: progress | stall | regress | complete.
func taxoAccept(outcome string) { taxoRecord("accept:" + outcome) }

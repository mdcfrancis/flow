package appgen

import (
	"context"
	"fmt"

	"github.com/mdcfrancis/flow/compiler"
	"github.com/mdcfrancis/flow/evolution"
)

// PromotePrimitive is the ACCEPTANCE GATE for adding a private-page primitive: a candidate WAT
// is enrolled only if it compiles AND fully passes a behavior-pinning acceptance suite. It is
// the single chokepoint the system uses both at boot (for the built-in dict/list/set) and at
// runtime (when a self-extension proposes a new structure) — a proposed primitive earns its
// place only by being correct, never by assertion. Returns the compiled bytecode and a verdict.
//
// Pure page primitives dispatch nothing, so no resolver is needed; the suite runs against the
// candidate's own window state (see DictAcceptance/ListAcceptance/SetAcceptance).
func PromotePrimitive(ctx context.Context, wat string, suite *evolution.AcceptanceSuite, payloadOffset, stateWindow uint32) (bytecode []byte, ok bool, passed, total int, reason string) {
	art, err := compiler.NewCompilerService().CompileGenotype(wat)
	if err != nil || !art.SyntaxPassed {
		return nil, false, 0, 0, fmt.Sprintf("does not compile: %v (%s)", err, art.ErrorContext)
	}
	if suite == nil || len(suite.Scenarios) == 0 {
		return art.Bytecode, false, 0, 0, "no acceptance suite — a primitive must be pinned by behavior"
	}
	passed, total = evolution.ScoreSuite(ctx, art.Bytecode, "run-tick", suite, payloadOffset, stateWindow, nil)
	if total > 0 && passed == total {
		return art.Bytecode, true, passed, total, "passes acceptance"
	}
	return art.Bytecode, false, passed, total, fmt.Sprintf("fails acceptance (%d/%d)", passed, total)
}

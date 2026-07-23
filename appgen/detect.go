package appgen

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// CellWAT pairs a cell URN with its genotype for corpus-level pattern analysis.
type CellWAT struct {
	URN string
	WAT string
}

// SharedStructureProposal names a data-structure primitive that several cells could SHARE — the
// recurring-pattern detector's output. It closes the self-extension loop autonomously: the named
// cells are flagged structural so they refactor to dispatch to the primitive; its ABI is
// published as system guidance so the FIRST cell mints it (witnessed by its tapes) and the rest
// reuse it. Cost stays the driver — the detector runs over the corpus and the mint/gate machinery
// (PromotePrimitive / witnessed minting) still guards correctness.
type SharedStructureProposal struct {
	URN       string   `json:"urn"`
	Ops       string   `json:"ops"`
	Cells     []string `json:"cells"`
	Rationale string   `json:"rationale"`
}

const detectStructurePrompt = `You are the RECURRING-PATTERN DETECTOR for a self-extending system.
You are given several cells (URN + WAT). Decide whether TWO OR MORE of them implement the SAME
reusable shape that a NEW shared data-structure primitive would serve — so the repeated logic
becomes ONE dispatched primitive instead of N copies (lower total tokens and fuel).

Only propose when the win is REAL and served by NONE of the existing primitives: sys:dict
(uint32->uint32 map), sys:set (membership), sys:list (growable array), sys:map/fold/filter/scan/
zip/iterate (array combinators). A new primitive must be pure ops over a private page, opcode-in-
args like the others.

Respond with EXACTLY one JSON object and nothing else:
{"urn":"urn:hdm:sys:<name>","ops":"<one line: the op ABI, e.g. [op,key,val] op0=get op1=insert>","cells":["<urn>","<urn>"],"rationale":"<one line: the shared shape>"}
If no shared primitive beyond the existing library is warranted, respond {}.`

// DetectSharedStructure asks the model whether >= 2 of the given cells share a promotable shape a
// NEW primitive would serve. It GROUNDS the result — the URN must be a sys:* primitive and it must
// name >= 2 cells that were actually in the input — so a hallucinated proposal is dropped. Returns
// nil (no proposal) rather than an error for the common "nothing to promote" case.
func (g *Grower) DetectSharedStructure(ctx context.Context, cells []CellWAT) (*SharedStructureProposal, error) {
	if len(cells) < 2 || g.model == nil {
		return nil, nil
	}
	var pb strings.Builder
	for _, c := range cells {
		wat := c.WAT
		if len(wat) > 1200 {
			wat = wat[:1200]
		}
		fmt.Fprintf(&pb, "CELL %s:\n%s\n\n", c.URN, wat)
	}
	resp, err := g.model.InvokeReasoning(ctx, g.prompt("detect-structure", detectStructurePrompt), pb.String())
	if err != nil {
		return nil, err
	}
	js := extractJSON(resp)
	if js == "" {
		return nil, nil
	}
	var p SharedStructureProposal
	if json.Unmarshal([]byte(js), &p) != nil {
		return nil, nil
	}
	if !strings.HasPrefix(p.URN, "urn:hdm:sys:") {
		return nil, nil // must be a sys primitive; drop hallucinations
	}
	known := make(map[string]bool, len(cells))
	for _, c := range cells {
		known[c.URN] = true
	}
	var grounded []string
	seen := map[string]bool{}
	for _, u := range p.Cells {
		if known[u] && !seen[u] {
			grounded = append(grounded, u)
			seen[u] = true
		}
	}
	if len(grounded) < 2 {
		return nil, nil // a shared primitive needs >= 2 real users
	}
	p.Cells = grounded
	return &p, nil
}

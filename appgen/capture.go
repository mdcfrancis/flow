package appgen

import (
	"strings"

	"github.com/mdcfrancis/flow/evolution"
)

// CaptureExample promotes a GREEN cell to a worked example in the knowledge base — the
// self-improvement loop: the system learns from its own successes, so a future cell of
// the same kind and intent retrieves this solution. The caller confirms the cell is
// green and passes its "pass/total" score. Reads the cell's kind + semantics + ports
// from the envelope and its WAT from the repo, then AddExample (novelty-gated, so
// re-capture is a cheap no-op). Returns whether it was stored.
func (g *Grower) CaptureExample(urn, score string) (bool, error) {
	ns := evolution.AppNamespaceOf(urn)
	env := LoadEnvelope(g.ledger, ns)
	if env == nil {
		return false, nil
	}
	var sub *Subsystem
	for i := range env.SubsystemRequirements {
		if env.SubsystemRequirements[i].Identity == urn {
			sub = &env.SubsystemRequirements[i]
			break
		}
	}
	if sub == nil {
		return false, nil // fractured child or unknown cell — nothing to attribute
	}
	desc, err := g.repo.Load(urn)
	if err != nil {
		return false, err
	}
	wat, err := g.repo.Genotype(desc)
	if err != nil || wat == "" {
		return false, err
	}
	// Under P0 the stored genome is the SURFACE source; tag the captured example with
	// its language so it lands in the right retrieval bucket: "(module" → raw WAT,
	// "(cell" → S-expression Flux, otherwise the concatenative Forth surface (a word
	// stream). This keeps a captured green cell superseding the seed example in the
	// SAME surface.
	src := strings.TrimSpace(wat)
	lang := "forth"
	switch {
	case strings.HasPrefix(src, "(module"):
		lang = ""
	case strings.HasPrefix(src, "(cell"):
		lang = "flux"
	}
	return evolution.AddExample(g.ledger, evolution.Example{
		Kind:       string(kindOf(*sub)),
		Entry:      entryFor(*sub),
		Lang:       lang,
		Semantics:  sub.Semantics,
		Reads:      sub.Reads,
		Writes:     sub.Writes,
		WAT:        wat,
		Score:      score,
		Provenance: urn,
	})
}

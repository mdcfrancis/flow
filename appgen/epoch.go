package appgen

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mdcfrancis/flow/evolution"
	"github.com/mdcfrancis/flow/manifest"
)

// An EPOCH is a self-contained checkpoint of the evolvable SYSTEM state — the sys: WAT cells,
// the refined prompts, and the guidance constitution — so that as the system gets better it
// can be frozen into a new baseline and future runs start from it rather than from the
// hand-written defaults. It snapshots the SYSTEM, not the applications: app cells are left
// alone. Self-contained (stores content, not just hashes) so it survives GC.
type Epoch struct {
	Name     string                     `json:"name"`
	Prompts  map[string]string          `json:"prompts"`  // prompt name -> refined override text
	Guidance string                     `json:"guidance"` // the system Guidance, marshalled
	Policy   *evolution.Policy          `json:"policy,omitempty"`
	CheckThr *evolution.CheckThresholds `json:"checkThresholds,omitempty"`
	SysCells map[string]string          `json:"sysCells"` // urn:hdm:sys:* -> WAT genotype
}

const sysCellPrefix = "urn:hdm:sys:"

func epochRef(name string) string { return "urn:hdm:system:epoch:" + name }

// SaveEpoch snapshots the current evolvable system state into a named baseline.
func (g *Grower) SaveEpoch(name string) (*Epoch, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("epoch name required")
	}
	ep := &Epoch{Name: name, Prompts: map[string]string{}, SysCells: map[string]string{}}
	for _, pn := range evolution.ListPromptOverrides(g.ledger) {
		ep.Prompts[pn] = evolution.LoadPromptOverride(g.ledger, pn)
	}
	if gd := evolution.LoadGuidance(g.ledger, evolution.SystemGuidanceKey); len(gd.Entries) > 0 {
		if raw, err := json.Marshal(gd); err == nil {
			ep.Guidance = string(raw)
		}
	}
	if evolution.PolicyOverridden(g.ledger) {
		p := evolution.LoadPolicy(g.ledger)
		ep.Policy = &p
	}
	ct := evolution.LoadCheckThresholds(g.ledger)
	ep.CheckThr = &ct
	if refs, err := g.ledger.Refs(); err == nil {
		for urn := range refs {
			if !strings.HasPrefix(urn, sysCellPrefix) {
				continue
			}
			if desc, err := g.repo.Load(urn); err == nil {
				if wat, gerr := g.repo.Genotype(desc); gerr == nil && strings.TrimSpace(wat) != "" {
					ep.SysCells[urn] = wat
				}
			}
		}
	}
	raw, err := json.Marshal(ep)
	if err != nil {
		return nil, err
	}
	h, err := g.ledger.WriteBlock(raw)
	if err != nil {
		return nil, err
	}
	if err := g.ledger.UpdateRef(epochRef(name), h); err != nil {
		return nil, err
	}
	g.event("epoch", name, fmt.Sprintf("checkpoint: %d prompt(s), guidance=%v, %d sys cell(s)",
		len(ep.Prompts), ep.Guidance != "", len(ep.SysCells)))
	return ep, nil
}

// LoadEpoch returns a saved epoch by name.
func (g *Grower) LoadEpoch(name string) (*Epoch, error) {
	h, err := g.ledger.GetRef(epochRef(name))
	if err != nil {
		return nil, fmt.Errorf("no epoch %q", name)
	}
	raw, err := g.ledger.ReadBlock(h)
	if err != nil {
		return nil, err
	}
	var ep Epoch
	if err := json.Unmarshal(raw, &ep); err != nil {
		return nil, err
	}
	return &ep, nil
}

// ListEpochs returns the names of saved epochs.
func (g *Grower) ListEpochs() []string {
	refs, err := g.ledger.Refs()
	if err != nil {
		return nil
	}
	prefix := epochRef("")
	var out []string
	for urn := range refs {
		if strings.HasPrefix(urn, prefix) {
			out = append(out, strings.TrimPrefix(urn, prefix))
		}
	}
	return out
}

// RestoreEpoch makes a saved epoch the current SYSTEM baseline: it applies the epoch's prompt
// overrides (clearing any not in the epoch), its guidance, and re-seeds its sys cells.
// Application cells are untouched. Prompts + guidance take effect immediately (resolved from
// the ledger on each use); re-seeded sys cells take effect on the next boot/reseed. Returns a
// summary.
func (g *Grower) RestoreEpoch(name string) (string, error) {
	ep, err := g.LoadEpoch(name)
	if err != nil {
		return "", err
	}
	// Prompts: clear overrides not in the epoch, then set the epoch's.
	for _, pn := range evolution.ListPromptOverrides(g.ledger) {
		if _, ok := ep.Prompts[pn]; !ok {
			_ = evolution.SavePromptOverride(g.ledger, pn, "")
		}
	}
	for pn, text := range ep.Prompts {
		_ = evolution.SavePromptOverride(g.ledger, pn, text)
	}
	// Guidance: replace wholesale.
	var gd evolution.Guidance
	if ep.Guidance != "" {
		_ = json.Unmarshal([]byte(ep.Guidance), &gd)
	}
	_ = evolution.SaveGuidance(g.ledger, evolution.SystemGuidanceKey, &gd)
	if ep.Policy != nil {
		_ = evolution.SavePolicy(g.ledger, *ep.Policy)
	}
	if ep.CheckThr != nil {
		_ = evolution.SaveCheckThresholds(g.ledger, *ep.CheckThr) // gated; a saved epoch's thresholds already passed
	}
	// Sys cells: recompile + re-seed.
	seeded := 0
	for urn, wat := range ep.SysCells {
		art, cerr := g.sieve.CompileGenotype(wat)
		if cerr != nil || !art.SyntaxPassed {
			continue
		}
		sem := manifest.SemanticManifest{FunctionalIntent: "system library cell (restored from epoch " + name + ")"}
		if h, _, perr := g.repo.PutCell(urn, wat, art.Bytecode, sem, 0); perr == nil {
			_ = g.repo.SeedRef(urn, h)
			seeded++
		}
	}
	g.event("epoch", name, "restored as baseline")
	return fmt.Sprintf("restored epoch %q: %d prompt(s), guidance=%v, %d/%d sys cell(s) re-seeded (restart to reload sys cells into the runtime)",
		name, len(ep.Prompts), ep.Guidance != "", seeded, len(ep.SysCells)), nil
}

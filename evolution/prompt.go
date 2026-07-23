package evolution

import (
	"encoding/json"
	"strings"

	"github.com/mdcfrancis/flow/storage"
)

// The system's behavior is largely encoded in hand-written PROMPTS. This makes them
// overridable, LLM-refinable artifacts: a use-site calls ResolvePrompt(name, default) and
// gets a stored override if one exists, else the hand-written default. An optimizer can then
// rewrite a prompt from feedback and persist the override — the system tuning its own prompts.

func promptRef(name string) string { return "urn:hdm:system:prompt:" + name }

// SavePromptOverride stores a refined prompt under name (empty text clears the override,
// reverting to the hand-written default).
func SavePromptOverride(ledger *storage.LedgerEngine, name, text string) error {
	if strings.TrimSpace(text) == "" {
		return ledger.DeleteRefs(promptRef(name))
	}
	h, err := ledger.WriteBlock([]byte(text))
	if err != nil {
		return err
	}
	return ledger.UpdateRef(promptRef(name), h)
}

// LoadPromptOverride returns the stored override for name, or "" if none.
func LoadPromptOverride(ledger *storage.LedgerEngine, name string) string {
	h, err := ledger.GetRef(promptRef(name))
	if err != nil {
		return ""
	}
	raw, err := ledger.ReadBlock(h)
	if err != nil {
		return ""
	}
	return string(raw)
}

// ResolvePrompt returns the override for name if present, else the hand-written default.
func ResolvePrompt(ledger *storage.LedgerEngine, name, def string) string {
	if ledger == nil {
		return def
	}
	if o := LoadPromptOverride(ledger, name); o != "" {
		return o
	}
	return def
}

func promptGrievanceRef(name string) string { return "urn:hdm:system:prompt-grievance:" + name }

const maxGrievances = 20

// AddPromptGrievance records a concrete, objective complaint about a prompt's OUTPUT — most
// usefully a malformed/invalid result attributable to the prompt (bad JSON, fields it invented,
// a cell it hallucinated). Accumulated grievances auto-trigger refinement of that prompt.
// De-duplicated and capped.
func AddPromptGrievance(ledger *storage.LedgerEngine, name, example string) {
	example = strings.TrimSpace(example)
	if ledger == nil || example == "" {
		return
	}
	g := LoadPromptGrievances(ledger, name)
	for _, e := range g {
		if e == example {
			return
		}
	}
	g = append(g, example)
	if len(g) > maxGrievances {
		g = g[len(g)-maxGrievances:]
	}
	if raw, err := json.Marshal(g); err == nil {
		if h, werr := ledger.WriteBlock(raw); werr == nil {
			_ = ledger.UpdateRef(promptGrievanceRef(name), h)
		}
	}
}

// LoadPromptGrievances returns the accumulated grievances for a prompt.
func LoadPromptGrievances(ledger *storage.LedgerEngine, name string) []string {
	h, err := ledger.GetRef(promptGrievanceRef(name))
	if err != nil {
		return nil
	}
	raw, err := ledger.ReadBlock(h)
	if err != nil {
		return nil
	}
	var g []string
	if json.Unmarshal(raw, &g) != nil {
		return nil
	}
	return g
}

// ClearPromptGrievances drops a prompt's grievances (after it has been refined).
func ClearPromptGrievances(ledger *storage.LedgerEngine, name string) {
	_ = ledger.DeleteRefs(promptGrievanceRef(name))
}

// ListPromptOverrides returns the names of prompts that currently have a stored override.
func ListPromptOverrides(ledger *storage.LedgerEngine) []string {
	refs, err := ledger.Refs()
	if err != nil {
		return nil
	}
	const prefix = "urn:hdm:system:prompt:"
	var names []string
	for urn := range refs {
		if strings.HasPrefix(urn, prefix) {
			names = append(names, strings.TrimPrefix(urn, prefix))
		}
	}
	return names
}

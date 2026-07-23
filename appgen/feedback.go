package appgen

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mdcfrancis/flow/evolution"
)

// RoutedFeedback is one piece of operator commentary after routing: the scope it applies
// to and the rewritten, injectable statement.
type RoutedFeedback struct {
	Scope     string `json:"scope"`     // "system" (every app) or "app" (this app)
	Statement string `json:"statement"` // concise imperative principle/criterion
	New       bool   `json:"new"`       // false if a duplicate of existing guidance
}

const feedbackPrompt = `You route free-form operator COMMENTARY about a solution into durable GUIDANCE.
Separate concerns and rewrite each into a concise, imperative principle:

- scope "system": a GENERAL principle that should hold for ANY application this system builds
  (e.g. "renderers must map values to a high-contrast palette", "coordinate through shared
  state, never private buffers"). Applies to every future app.
- scope "app": specific to the CURRENT application's goal (e.g. "the Mandelbrot's interior
  should be pure black", "the paddle should move faster"). Applies only to this app.

Rules:
- One statement per distinct point; split compound commentary.
- Rewrite as an actionable directive, not a restatement of the complaint ("colors are dim" ->
  "map escape counts to a bright, high-contrast gradient so structure is visible").
- If a point is a broad principle disguised as an app complaint, scope it "system".
- If there is no current application, scope everything "system".
- Output ONLY JSON, no prose or fences:
{"guidance":[{"scope":"system","statement":"..."},{"scope":"app","statement":"..."}]}`

// RouteFeedback classifies operator commentary into SYSTEM-scoped principles (every app) and
// APP-scoped guidance (this application), rewrites each into an injectable statement, and
// persists them to the guidance stores. The human supplies criteria; an adversarial critic
// judges — so there is no confirmation step. namespace may be "" (everything routes to
// system). Returns the routed entries.
func (g *Grower) RouteFeedback(ctx context.Context, namespace, commentary string) ([]RoutedFeedback, error) {
	commentary = strings.TrimSpace(commentary)
	if commentary == "" {
		return nil, fmt.Errorf("empty commentary")
	}
	resp, err := g.model.InvokeReasoning(ctx, g.prompt("feedback", feedbackPrompt), feedbackPayload(namespace, commentary))
	if err != nil {
		return nil, err
	}
	js := extractJSON(resp)
	if js == "" {
		evolution.AddPromptGrievance(g.ledger, "feedback", `output contained no JSON object matching {"guidance":[{"scope","statement"}]}`)
		return nil, fmt.Errorf("no routing in model output")
	}
	var out struct {
		Guidance []RoutedFeedback `json:"guidance"`
	}
	if err := json.Unmarshal([]byte(js), &out); err != nil {
		evolution.AddPromptGrievance(g.ledger, "feedback", "output was not valid JSON matching the {guidance:[{scope,statement}]} schema")
		return nil, fmt.Errorf("decode routing: %w", err)
	}
	sys := evolution.LoadGuidance(g.ledger, evolution.SystemGuidanceKey)
	var app *evolution.Guidance
	if namespace != "" {
		app = evolution.LoadGuidance(g.ledger, evolution.AppGuidanceKey(namespace))
	}
	routed := []RoutedFeedback{}
	sysChanged, appChanged := false, false
	for _, r := range out.Guidance {
		stmt := strings.TrimSpace(r.Statement)
		if stmt == "" {
			continue
		}
		scope := strings.ToLower(strings.TrimSpace(r.Scope))
		if scope != "app" || namespace == "" || app == nil {
			scope = "system"
		}
		if scope == "system" {
			_, isNew := sys.Add(stmt)
			sysChanged = sysChanged || isNew
			routed = append(routed, RoutedFeedback{Scope: "system", Statement: stmt, New: isNew})
		} else {
			_, isNew := app.Add(stmt)
			appChanged = appChanged || isNew
			routed = append(routed, RoutedFeedback{Scope: "app", Statement: stmt, New: isNew})
		}
	}
	if sysChanged {
		if err := evolution.SaveGuidance(g.ledger, evolution.SystemGuidanceKey, sys); err != nil {
			return routed, err
		}
	}
	if appChanged && app != nil {
		if err := evolution.SaveGuidance(g.ledger, evolution.AppGuidanceKey(namespace), app); err != nil {
			return routed, err
		}
	}
	for _, r := range routed {
		g.event("feedback", namespace, fmt.Sprintf("guidance (%s): %s", r.Scope, r.Statement))
	}
	return routed, nil
}

func feedbackPayload(namespace, commentary string) string {
	var b strings.Builder
	if namespace != "" {
		b.WriteString("CURRENT APPLICATION: ")
		b.WriteString(namespace)
		b.WriteByte('\n')
	} else {
		b.WriteString("CURRENT APPLICATION: (none focused — route everything to system)\n")
	}
	b.WriteString("\nOPERATOR COMMENTARY:\n")
	b.WriteString(commentary)
	return b.String()
}

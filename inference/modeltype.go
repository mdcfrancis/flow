package inference

import (
	"context"
	"os"
	"strings"
)

// ModelType is a logical model ROLE — a fixed, extensible enumeration that lets
// each faculty (and, via the cell-kind ontology, each cell) run on the model best
// suited to it, and lets model choice be priced into the cost function. It mirrors
// appgen.CellKind: a closed set today (so every switch stays total), extended by a
// single registry entry.
type ModelType string

const (
	ModelReason ModelType = "reason" // architecture, planning, fracture, critique — strong reasoning
	ModelCode   ModelType = "code"   // WAT synthesis + code edits — coding
	ModelVision ModelType = "vision" // rendered-frame critique — multimodal
	ModelFast   ModelType = "fast"   // cheap gates / classification — speed + low cost
)

// AllModelTypes lists the types in a stable order (registry iteration, boot logs).
var AllModelTypes = []ModelType{ModelReason, ModelCode, ModelVision, ModelFast}

// modelTypeSpec is a type's policy: what it's for and its cost tier.
type modelTypeSpec struct {
	Capability string
	// CostWeight prices a token from this type RELATIVE to a baseline of 1.0, so a
	// frame that reached for an expensive type is charged more in the Hamiltonian's
	// token term. Tunable (later policy-backed); the ordering is what matters:
	// fast < code ≈ vision < reason.
	CostWeight float64
}

var modelTypes = map[ModelType]modelTypeSpec{
	ModelReason: {"architecture, planning, fracture, critique", 2.0},
	ModelCode:   {"WAT synthesis and code edits", 1.0},
	ModelVision: {"rendered-frame critique (multimodal)", 1.0},
	ModelFast:   {"cheap gates and classification", 0.5},
}

// Valid reports whether t is a known model type.
func (t ModelType) Valid() bool { _, ok := modelTypes[t]; return ok }

// CostWeight returns the type's relative token cost (1.0 for an unknown type, so a
// bad value never zeroes the cost).
func (t ModelType) CostWeight() float64 {
	if s, ok := modelTypes[t]; ok {
		return s.CostWeight
	}
	return 1.0
}

// Capability returns the human-readable role description.
func (t ModelType) Capability() string { return modelTypes[t].Capability }

// TypedModelEnv returns the per-type overrides from HDM_LLM_MODEL_<TYPE> /
// HDM_LLM_URL_<TYPE> / HDM_LLM_PROVIDER_<TYPE> (TYPE uppercased). An empty string
// means "fall back to the base model", so an unconfigured type is identical to the
// single-model setup — the whole feature is opt-in per type.
func TypedModelEnv(t ModelType) (model, url, provider string) {
	s := strings.ToUpper(string(t))
	return os.Getenv("HDM_LLM_MODEL_" + s),
		os.Getenv("HDM_LLM_URL_" + s),
		os.Getenv("HDM_LLM_PROVIDER_" + s)
}

// ModelRouter resolves a logical role to a concrete client. It satisfies the
// Reasoner boundary itself (InvokeReasoning delegates to the reason/base client), so
// it can be passed anywhere a single model is expected; call sites that care select a
// type with For(). Every type with no override resolves to the BASE client, so a
// router with no HDM_LLM_MODEL_* set behaves exactly like the single base model.
type ModelRouter struct {
	base    *LocalModelClient
	clients map[ModelType]*LocalModelClient
}

// NewModelRouter builds a router over a base client and a per-type client map.
// Missing/nil entries fall back to base. The map is provided by the caller (which
// knows how to construct a client for a given provider/model/url) so this package
// stays free of boot/config policy.
func NewModelRouter(base *LocalModelClient, perType map[ModelType]*LocalModelClient) *ModelRouter {
	r := &ModelRouter{base: base, clients: make(map[ModelType]*LocalModelClient, len(AllModelTypes))}
	for _, t := range AllModelTypes {
		if c := perType[t]; c != nil {
			r.clients[t] = c
		} else {
			r.clients[t] = base
		}
	}
	return r
}

// For returns the client bound to a role. An unknown role or a type that resolved
// to nothing falls back to the base client — routing is always safe.
func (r *ModelRouter) For(role string) *LocalModelClient {
	if r == nil {
		return nil
	}
	if c, ok := r.clients[ModelType(strings.ToLower(strings.TrimSpace(role)))]; ok && c != nil {
		return c
	}
	return r.base
}

// InvokeReasoning makes the router a Reasoner in its own right, defaulting to the
// reason type — so it drops in wherever a single model is expected today.
func (r *ModelRouter) InvokeReasoning(ctx context.Context, systemPrompt, userContext string) (string, error) {
	return r.For(string(ModelReason)).InvokeReasoning(ctx, systemPrompt, userContext)
}

// distinct returns each underlying client once (types often share the base client).
func (r *ModelRouter) distinct() []*LocalModelClient {
	seen := map[*LocalModelClient]bool{}
	var out []*LocalModelClient
	add := func(c *LocalModelClient) {
		if c != nil && !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	add(r.base)
	for _, t := range AllModelTypes {
		add(r.clients[t])
	}
	return out
}

// TotalTokens sums cumulative token usage across every distinct client, so token
// accounting (the Hamiltonian's cost feed) stays correct once calls fan out across
// types.
func (r *ModelRouter) TotalTokens() uint64 {
	var n uint64
	for _, c := range r.distinct() {
		n += c.TotalTokens()
	}
	return n
}

// SetObserve installs the same data-flow observer on every distinct client, so the
// live log captures routed calls regardless of which type served them.
func (r *ModelRouter) SetObserve(fn func(purpose string, promptBytes, respBytes, tokens int, ms int64)) {
	for _, c := range r.distinct() {
		c.Observe = fn
	}
}

// Bindings returns, in stable order, the resolved (type, model) pairs — for the
// boot log so the active routing is visible.
func (r *ModelRouter) Bindings() []struct{ Type, Model string } {
	out := make([]struct{ Type, Model string }, 0, len(AllModelTypes))
	for _, t := range AllModelTypes {
		out = append(out, struct{ Type, Model string }{string(t), r.clients[t].model})
	}
	return out
}

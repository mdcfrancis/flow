package inference

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
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

// costWeightOverride holds the ledger-policy cost-weight overrides (map[ModelType]
// float64), copy-on-write so CostWeight reads lock-free while the fixpoint updates it.
var costWeightOverride atomic.Value

// SetCostWeights replaces the per-type cost-weight overrides (a nil/empty map clears
// them). Applied from the ledger policy at boot and each fixpoint, so the cost tiers
// are runtime-tunable.
func SetCostWeights(w map[ModelType]float64) {
	m := make(map[ModelType]float64, len(w))
	for k, v := range w {
		m[k] = v
	}
	costWeightOverride.Store(m)
}

// CostWeight returns the type's relative token cost: the ledger-policy override if
// set, else the built-in tier (1.0 for an unknown type, so a bad value never zeroes
// the cost).
func (t ModelType) CostWeight() float64 {
	if v := costWeightOverride.Load(); v != nil {
		if w, ok := v.(map[ModelType]float64)[t]; ok {
			return w
		}
	}
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
	mu      sync.RWMutex // guards clients (Rebind writes; readers RLock)
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
	r.mu.RLock()
	c, ok := r.clients[ModelType(strings.ToLower(strings.TrimSpace(role)))]
	r.mu.RUnlock()
	if ok && c != nil {
		return c
	}
	return r.base
}

// Rebind swaps the client serving a type at runtime (e.g. after a ledger-policy
// binding change) and returns the previous client so the caller may Unload it. A nil
// client resets the type to the base.
func (r *ModelRouter) Rebind(t ModelType, c *LocalModelClient) *LocalModelClient {
	if c == nil {
		c = r.base
	}
	r.mu.Lock()
	old := r.clients[t]
	r.clients[t] = c
	r.mu.Unlock()
	return old
}

// ModelOf returns the model id currently bound to a type ("" if none).
func (r *ModelRouter) ModelOf(t ModelType) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if c := r.clients[t]; c != nil {
		return c.model
	}
	return ""
}

// InUse reports whether a client is still referenced (the base or any type), so a
// caller can decide it is safe to Unload a rebound-away client.
func (r *ModelRouter) InUse(c *LocalModelClient) bool {
	if c == nil {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.base == c {
		return true
	}
	for _, cl := range r.clients {
		if cl == c {
			return true
		}
	}
	return false
}

// Model returns the model id this client serves.
func (c *LocalModelClient) Model() string { return c.model }

// InvokeReasoning makes the router a Reasoner in its own right, defaulting to the
// reason type — so it drops in wherever a single model is expected today.
func (r *ModelRouter) InvokeReasoning(ctx context.Context, systemPrompt, userContext string) (string, error) {
	return r.For(string(ModelReason)).InvokeReasoning(ctx, systemPrompt, userContext)
}

// distinct returns each underlying client once (types often share the base client).
func (r *ModelRouter) distinct() []*LocalModelClient {
	r.mu.RLock()
	defer r.mu.RUnlock()
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

// WeightedTokens returns the cognitive spend weighted by model TIER: each distinct
// client's tokens times the cost weight of the type bound to it (the base/default at
// the reason tier). This is the type-aware cost the budget/tuning logic reads, so an
// expensive reasoner's tokens count for more than a fast model's. Exact when types
// bind distinct models (the mixed-model case); a type sharing the base folds into it
// at the reason weight. Equals raw tokens × reason-weight in the single-model case.
func (r *ModelRouter) WeightedTokens() float64 {
	r.mu.RLock()
	defer r.mu.RUnlock()
	seen := map[*LocalModelClient]bool{}
	var total float64
	if r.base != nil {
		total += float64(r.base.TotalTokens()) * ModelReason.CostWeight()
		seen[r.base] = true
	}
	for _, t := range AllModelTypes {
		c := r.clients[t]
		if c == nil || seen[c] {
			continue
		}
		seen[c] = true
		total += float64(c.TotalTokens()) * t.CostWeight()
	}
	return total
}

// Unload asks the local server to evict this client's model from memory
// (POST /v1/models/{model}/unload) — freeing RAM when several models are bound to
// different types. No-op for Gemini (nothing local to unload). Best-effort.
func (c *LocalModelClient) Unload(ctx context.Context) error {
	if c == nil || c.provider == providerGemini {
		return nil
	}
	u := c.baseURL + "/v1/models/" + url.PathEscape(c.model) + "/unload"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, nil)
	if err != nil {
		return err
	}
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unload %s: status %d: %s", c.model, resp.StatusCode, string(body))
	}
	return nil
}

// UnloadAll evicts every distinct model the router holds, freeing local memory —
// e.g. on shutdown when HDM loaded several models across types. Best-effort.
func (r *ModelRouter) UnloadAll(ctx context.Context) {
	for _, c := range r.distinct() {
		_ = c.Unload(ctx)
	}
}

// TokensByType returns the raw token count for each type that binds a DISTINCT client
// (a type sharing the base is omitted — its tokens are counted on the base). For the
// per-type spend readout.
func (r *ModelRouter) TokensByType() map[ModelType]uint64 {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[ModelType]uint64, len(AllModelTypes))
	for _, t := range AllModelTypes {
		if c := r.clients[t]; c != nil && c != r.base {
			out[t] = c.TotalTokens()
		}
	}
	return out
}

// Bindings returns, in stable order, the resolved (type, model) pairs — for the
// boot log so the active routing is visible.
func (r *ModelRouter) Bindings() []struct{ Type, Model string } {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]struct{ Type, Model string }, 0, len(AllModelTypes))
	for _, t := range AllModelTypes {
		out = append(out, struct{ Type, Model string }{string(t), r.clients[t].model})
	}
	return out
}

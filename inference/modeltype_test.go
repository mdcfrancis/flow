package inference

import (
	"context"
	"os"
	"testing"
)

func TestModelTypeRegistry(t *testing.T) {
	for _, mt := range AllModelTypes {
		if !mt.Valid() {
			t.Errorf("%s should be valid", mt)
		}
		if mt.Capability() == "" {
			t.Errorf("%s missing capability", mt)
		}
	}
	// Cost ordering is the whole point: fast < code ≈ vision < reason.
	if !(ModelFast.CostWeight() < ModelCode.CostWeight() &&
		ModelCode.CostWeight() <= ModelVision.CostWeight() &&
		ModelVision.CostWeight() < ModelReason.CostWeight()) {
		t.Fatalf("cost tiers out of order: fast=%v code=%v vision=%v reason=%v",
			ModelFast.CostWeight(), ModelCode.CostWeight(), ModelVision.CostWeight(), ModelReason.CostWeight())
	}
	// Unknown type is safe (weight 1.0, not zero).
	if ModelType("bogus").Valid() || ModelType("bogus").CostWeight() != 1.0 {
		t.Fatal("unknown type must be invalid with a non-zero default weight")
	}
}

func TestTypedModelEnv(t *testing.T) {
	t.Setenv("HDM_LLM_MODEL_CODE", "coder-x")
	t.Setenv("HDM_LLM_URL_CODE", "http://localhost:9001")
	m, u, p := TypedModelEnv(ModelCode)
	if m != "coder-x" || u != "http://localhost:9001" || p != "" {
		t.Fatalf("env resolution wrong: model=%q url=%q provider=%q", m, u, p)
	}
	// An unset type resolves to empty (fall back to base).
	os.Unsetenv("HDM_LLM_MODEL_VISION")
	if m, _, _ := TypedModelEnv(ModelVision); m != "" {
		t.Fatalf("unset type must be empty, got %q", m)
	}
}

func TestModelRouterFallbackAndRouting(t *testing.T) {
	base := NewLocalModelClient("http://base", "base-model")
	coder := NewLocalModelClient("http://coder", "coder-model")
	router := NewModelRouter(base, map[ModelType]*LocalModelClient{ModelCode: coder})

	// Configured type routes to its client.
	if got := router.For("code"); got != coder {
		t.Fatal("code should route to the coder client")
	}
	// Unconfigured type falls back to base.
	if got := router.For("reason"); got != base {
		t.Fatal("unconfigured reason should fall back to base")
	}
	// Unknown role falls back to base.
	if got := router.For("nonsense"); got != base {
		t.Fatal("unknown role should fall back to base")
	}
	// Case/space tolerant.
	if got := router.For("  CODE "); got != coder {
		t.Fatal("role lookup should be case/space tolerant")
	}
	// The router itself satisfies the Reasoner shape (InvokeReasoning) — compile check.
	var _ reasonerShape = (*ModelRouter)(nil)
	var _ reasonerShape = (*LocalModelClient)(nil)
}

// reasonerShape mirrors evolution.Reasoner (which inference cannot import — that
// would cycle) so we can assert both the router and the client satisfy it.
type reasonerShape interface {
	InvokeReasoning(ctx context.Context, systemPrompt, userContext string) (string, error)
}

func TestModelRouterBindingsAndDistinct(t *testing.T) {
	base := NewLocalModelClient("http://base", "base-model")
	coder := NewLocalModelClient("http://coder", "coder-model")
	router := NewModelRouter(base, map[ModelType]*LocalModelClient{ModelCode: coder})

	// Two distinct underlying clients (base + coder); reason/vision/fast share base.
	if n := len(router.distinct()); n != 2 {
		t.Fatalf("distinct clients = %d, want 2", n)
	}
	b := router.Bindings()
	if len(b) != len(AllModelTypes) {
		t.Fatalf("bindings = %d, want %d", len(b), len(AllModelTypes))
	}
	got := map[string]string{}
	for _, x := range b {
		got[x.Type] = x.Model
	}
	if got["code"] != "coder-model" || got["reason"] != "base-model" {
		t.Fatalf("bindings wrong: %+v", got)
	}
}

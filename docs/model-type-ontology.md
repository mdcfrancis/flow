# Design spec: model-type ontology + cost-aware routing

> Status: design, not yet built. Working spec on `feat/model-type-ontology`.
> Fold or relocate at merge time (as we did with the cell-ontology plan).

## Problem / intent

Today every model call goes through **one** client with **one** model, wired once in
`buildModelClient()` (`main.go`) and handed to both `NewOrchestrator` and `NewGrower`.
But the calls are not the same kind of work:

- **WAT synthesis** (the sieve) wants a *coder*.
- **Architecture / planning / fracture / critique** want a *reasoner*.
- **Rendered-frame critique** wants a *multimodal* model.
- **Cheap gates** (difficulty yes/no, simple classification) want a *fast, cheap* model.

The local **omlx** server already serves specialised models (coder, thinking, vision,
several sizes). We want to (1) route each call to the right *logical model type*, and
(2) make the **model type part of the Hamiltonian cost** so the loop prefers the
cheapest type that still passes acceptance, and only "spends" on an expensive type
when it buys progress.

This mirrors the cell-type ontology (`appgen/kind.go`): a fixed-but-extensible
enumeration, declared/derived per use, that drives behavior as one authoritative fact.
The two ontologies meet — **a cell's `Kind` can select the model's type**.

## Design

### Model types (the enumeration)

A closed-but-extensible set, each binding a capability, a **cost tier**, and a concrete
model per provider:

| Type | Used for | Wants |
|---|---|---|
| `reason` | envelope, plan, fracture, difficulty-escalation, architecture/code critics | strong reasoning |
| `code` | the sieve (WAT synthesis), boundary/prompt edits | coding |
| `vision` | the visual critic (looks at rendered frames) | multimodal |
| `fast` | difficulty judgment, scenario/motion gates, cheap classification | speed/low cost |

Config via `HDM_LLM_MODEL_<TYPE>` (+ optional `HDM_LLM_URL_<TYPE>` /
`HDM_LLM_PROVIDER_<TYPE>` so a type can bind a *different provider* — e.g. local `code`,
Gemini `vision`). **Unset ⇒ the base model** (`HDM_LLM_MODEL` / today's default), so
existing single-model runs are unchanged.

### Routing

Two composable selectors:

- **By faculty (call site):** the sieve asks for `code`, `AuthorEnvelope`/`FractureCell`/
  critics ask for `reason`, the visual critic asks for `vision`, `JudgeDifficulty` asks
  for `fast`.
- **By cell kind (the "different parts of the simulation"):** in the synthesis path the
  target's `CellKind` picks the type — `render`→`vision`, `compute`/`leaf`/`input`→`code`,
  `compose`→(generated, no model). Kind override wins over the faculty default for the sieve.

### Model type as cost

`telemetry.SystemMetrics.TokenMilliCents` already feeds the Hamiltonian as
`Gamma·TokenMilliCents`. Make the fee **type-weighted**: each model call records
`(tokens, type)`, and a frame's `TokenMilliCents = Σ tokensᵢ · centsPerToken(typeᵢ)`,
where `centsPerToken` comes from the model-type registry. Nothing else in
`CalculateHamiltonian` changes — the loop already minimizes H, so it will now prefer the
cheaper type wherever acceptance is equal, and an expensive type must earn its cost in
acceptance/saliency.

### Why it's safe

The model choice is orthogonal to the verified substrate: whichever model writes or
judges a cell, the gauntlet + acceptance verify it deterministically. This is pure
outer-surface tuning — no new trust, consistent with "outer Go = security + rollback,"
and the type→model bindings + cost tiers can live in the evolvable policy (like the
Hamiltonian coefficients already do).

## Implementation stages

Each stage builds + tests green on its own; early stages are behavior-preserving
(single model until Stage 2 routes anything).

### Stage 1 — the ontology core (no behavior change)
- New `inference/modeltype.go`: `ModelType` (`reason|code|vision|fast`) + a registry
  `modelTypes` binding, per type: a capability string, `centsPerToken` (cost tier), and
  a resolver (env `HDM_LLM_MODEL_<TYPE>` / URL / provider, falling back to base).
- `deriveModelType(CellKind) ModelType` — the kind→type map (mirrors `appgen/kind.go`).
- Unit tests: registry resolution, env override, kind→type, fallback-to-base.

### Stage 2 — `ModelRouter` behind `Reasoner` (opt-in, still one model by default)
- `inference.ModelRouter`: holds a lazily-built client per `(type)` and exposes
  `For(ModelType) evolution.Reasoner`. Satisfies `Reasoner` itself (defaults to `reason`)
  so nothing downstream must change yet.
- `buildModelClient()` → `buildModelRouter()`; `NewOrchestrator`/`NewGrower` take the
  router. With no `HDM_LLM_MODEL_*` set, every type resolves to the base model — a
  verifiable no-op vs today.
- Log the resolved bindings at boot (`[COGNITION] types: reason=… code=… vision=… fast=…`).

### Stage 3 — route the faculties
- Thread a `ModelType` at the ~8–10 distinct call sites (via `router.For(t)`): sieve→`code`,
  envelope/plan/fracture/architecture-critic/code-critic→`reason`, visual critic→`vision`,
  `JudgeDifficulty`→`fast`, scenario/motion gates→`fast`.
- Keep the rest on the default. Verify each call site still compiles + tests pass with the
  base model; then a mixed-model boot shows each faculty using its bound model.

### Stage 4 — cell-kind → model-type in the sieve
- In the synthesis path, derive the sieve's model type from the target cell's `Kind`
  (`deriveModelType`), overriding the faculty default. This is where the two ontologies
  join: a `render` cell synthesises under `vision`, a `compute`/`leaf` under `code`.
- Test: a render target routes to the vision-bound model; a compute target to the code model.

### Stage 5 — model type in the cost
- Tag each model call's token usage with its `ModelType` (carry it on the client/router
  return path into the frame's telemetry).
- Compute `TokenMilliCents` as the type-weighted sum; carry through `FrameResult` /
  `FuelTracker` into `SystemMetrics`. Add `centsPerToken` to the registry with sane
  relative defaults (fast ≪ code ≈ vision < reason, tunable).
- Test: two identical frames differing only in model type produce different H; the cheaper
  type has lower H.

### Stage 6 — cost-driven escalation (builds on the existing stall ladder)
- Start a stalled synthesis on a cheaper type; on repeated stall, escalate the model type
  (`fast`→`code`→`reason`) — reuse the retry/`noteStructuralEscalation` machinery. The
  cost term is exactly what makes escalation pay for itself only when it yields acceptance.
- Test: a cell that a cheap model can't pass escalates and is logged; a cell a cheap model
  handles never escalates.

### Stage 7 — observability + evolvable surface
- `/perf` (or `/status`) reports per-type token spend and the active bindings; `[MODEL]`
  logs which type served a call and any escalation.
- Move the type→model bindings + cost tiers into the ledger-backed policy so routing is
  tunable (and later learnable from acceptance/cost outcomes), same pattern as the
  Hamiltonian coefficients.

## Verification

- Unit: registry/resolution, kind→type, type-weighted cost, escalation.
- `go build ./... && go vet ./... && go test ./...` green at every stage.
- End-to-end on omlx: a mixed-model run (e.g. `HDM_LLM_MODEL_CODE=Qwen3-Coder-Next-8bit`,
  `HDM_LLM_MODEL_REASON=Qwen3.6-35B-A3B-bf16`, `HDM_LLM_MODEL_VISION`=a Gemma vision model)
  growing the bouncing-balls app: logs show each faculty on its bound model, the cost
  reflects the types used, and the app still converges. Confirm a no-`*`-env run is
  behavior-identical to today.

## Key decisions / risks

- **Interface:** `router.For(type)` (call sites opt in) rather than adding a param to
  `Reasoner.InvokeReasoning` — avoids churning all 33 call sites; only the ~10 that care
  change.
- **Default preserves today.** Env unset ⇒ base model everywhere ⇒ a provable no-op until
  Stage 3+ is configured.
- **Cost calibration** (`centsPerToken` per type) is a judgement call; start with relative
  tiers and make them policy-tunable (Stage 7) rather than hard-coding forever.
- **Provider mixing** (local `code` + Gemini `vision`) is a feature, but keep vision's
  Gemini-only constraint in mind — the registry resolver handles per-type provider.

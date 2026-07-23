# Design spec: an example library + client-side agentic synthesis

> Status: design, not yet built. Working spec on `feat/agentic-examples`.
> Fold or relocate at merge time (as with the earlier plans).

## Problem

Synthesis is the bottleneck. On the simplest possible app — a *single bouncing
ball* — the local models plateaued: the renderer drew a circle at a fixed spot
instead of at the ball's coordinates (failed 4/5 position checks), and the physics
integrated but didn't bounce (2/10). The acceptance harness was *correct* — it caught
exactly these faults — but the models couldn't produce code that passes.

The recurring failure has a *shape*: "read a contract field, draw a primitive there",
"integrate a value and reflect it at a boundary", "read HMI input and move a field".
We already ship worked examples for the model to imitate — but they are **static and
generic**: `exampleRunTickWAT` / `exampleRenderFrameWAT` baked into `buildExamples`
(`evolution/orchestrator.go`). They draw a rect at *fixed* coordinates, so they never
teach *read-position-then-draw-there* — the very thing the ball needed.

Two facts point at the fix:
1. **Green cells are already worked examples** sitting in the ledger (a descriptor is
   `semantics + genotype(WAT) + acceptance score`). We just don't capture, retrieve, or
   reuse them.
2. The synthesis call is a **single-shot completion** (`Reasoner.InvokeReasoning`), so
   the model can't ask for a relevant example, or compile-check a draft and fix the
   error the assembler would have told it about.

## The idea

1. A **growable library of worked examples**: seeded with a few *simple canonical*
   cells, and **added to by the system itself** every time a cell goes green. The
   system builds a memory of its own solutions — the evolvable-surface philosophy
   applied to synthesis experience.
2. **Client-side agentic synthesis**: turn the sieve from a one-shot completion into a
   small tool-using agent that HDM orchestrates **in Go**. The model can call tools —
   `find_examples`, `list_examples`, `inspect_example`, and (the sleeper hit)
   `compile_check` — and HDM executes them locally and feeds the results back until the
   model returns a final cell.

**Client-side, not server-side.** HDM owns the agentic loop: it sends the tool
definitions, receives the model's `tool_calls`, executes each tool in Go, appends the
result as a `tool` message, and re-calls — looping until the model answers with no tool
calls. We do **not** use omlx's server-side agents (`/v1/mcp/*`, `/v1/responses` agent
mode). This keeps control, security, determinism, and rollback in the outer Go (the
house style), and works against any OpenAI-compatible `tools` endpoint — local or Gemini.

## Design

### Example store (`evolution` or a new `examples` package)

An `Example` is a captured, reusable solution:

```
Example {
  ID          string   // content hash
  Kind        CellKind // compute | render | input | leaf   (the ontology)
  Entry       string   // run-tick | render-frame
  Semantics   string   // one-line intent ("read player_x; draw a rect there")
  Reads/Writes []string // the contract shape it exercised
  WAT         string   // the solution genotype
  Score       string   // "5/5" — proof it is a WORKED example
  Provenance  string   // source cell URN / objective
  Tags        []string // "position-draw", "wall-bounce", "hmi-move", ...
}
```

Stored in the ledger under a stable ref namespace (`urn:hdm:examples:*`), mirroring
`SaveContract`/`SaveAppMap`. Content-addressed so identical solutions dedup.

### Seeding — the simple canonical set

Hand-write (or grow once and pin) a small set that covers the shapes the ball needed —
these double as regression fixtures and as the "cold-start" library:

- **read-field → draw circle there** (`render`): seed `x,y`; draw a circle at `(x,y)`.
- **integrate + wall bounce** (`compute`): `pos += vel`; reflect `vel` and clamp at 0
  and the screen bound.
- **read HMI key → move a field** (`input`): on a keydown, step a contract field, clamp.
- plus the existing hand-written cells (`life`, `router`, `ui`) tagged and registered.

### Capture — the self-improvement loop

When a cell reaches **green** (all acceptance passing), promote it to an example:
`captureExample(urn)` reads its descriptor + acceptance + plan and writes an `Example`.
Curate to keep the library *sharp*, not bloated:
- **Novelty gate**: only capture a (kind, shape, tag-set) not already well-covered, or
  that beats an existing example's score/size. Otherwise skip.
- Bounded per kind/tag (keep the best K), so retrieval stays high-signal.

### Retrieval

`findExamples(kind, semantics, reads, writes, k)` ranks the store by:
1. **kind match** (a `render` target only sees `render` examples) — hard filter;
2. **shape overlap** (shared contract-field roles / same entry / similar port count);
3. **semantic overlap** (keyword/tag overlap for Phase 1; embeddings later via omlx
   `/v1/embeddings`).

Returns the top-K worked examples, injected into `buildSeed` **in place of** the static
`buildExamples` (or alongside, when the store is thin).

### The client-side agentic loop (`inference` + `evolution`)

Extend the inference layer with an OpenAI-style tool-use round-trip and orchestrate the
loop in Go:

- `inference`: `InvokeTools(ctx, sys, user, tools []ToolDef, exec ToolExec, maxSteps int)
  (string, error)` — sends `tools` on `/v1/chat/completions`; while the response
  contains `tool_calls`, calls `exec(name, argsJSON)` (a Go callback), appends each
  result as a `tool` message, and re-requests; returns the final assistant text.
  Bounded by `maxSteps` and the reconnect/retry window. Falls back to a plain
  completion when the server/model reports no tool support.
- `evolution`: `RunAgenticSieve` wraps `RunSieve` — it provides the tool set (backed by
  the example store + the compiler) and runs `InvokeTools`, then feeds the model's final
  WAT through the same compile/verify gates as today. The acceptance gauntlet is
  unchanged: **tools inform synthesis; they never bypass verification.**

### The tools (all pure, all Go, all read-only except the returned WAT)

- `list_examples(kind?)` → ids + one-line semantics of available examples.
- `find_examples(query, kind?, k?)` → ranked worked examples (semantics + WAT + score).
- `inspect_example(id)` → the full WAT of one example.
- `compile_check(wat)` → runs the candidate through **our** WAT assembler
  (`compiler.CompileGenotype`) and returns `ok` or the exact error
  ("at most one table", "i32.div_s type mismatch: expected i32 got i64", …). This is
  what would have let Qwen-Coder self-correct the ABI violations it kept making.
- (optional) `contract_fields()` → the target app's shared-state field names/offsets, so
  a renderer reads the *right* address.

## Phasing

Each phase builds + tests green and is independently useful.

### Phase 1 — store + retrieval-augmented synthesis (no tool-calling)
- `examples` store (types, `SaveExample`/`LoadExamples`, content-addressed).
- Seed the simple canonical set (+ register the hand-written cells).
- `captureExample` on green (with the novelty gate), wired at the commit/convergence point.
- `findExamples` (kind + shape + keyword/tag ranking).
- Inject retrieved examples into `buildSeed`, replacing the static few-shot.
- **This alone should move the single-ball renderer** — a worked "draw at position"
  example is exactly the missing few-shot. Verify by re-running the ball.

### Phase 2 — client-side agentic tool loop
- `inference.InvokeTools` (OpenAI `tools` protocol, Go-orchestrated loop, graceful
  fallback). Probe which transport omlx honors most reliably (`chat/completions` tools).
- The tool set above, backed by the store + compiler.
- `RunAgenticSieve`; route hard/stalled cells through it (compose with the existing
  cost-driven escalation — escalate to *agentic + reasoner* on stall).
- Verify: a cell that fails one-shot converges agentically; `compile_check` visibly
  cuts ABI-violation dead-ends.

### Phase 3 — the loop gets more agentic (optional, later)
- More tools as they prove out: `inspect_cell(urn)`, `list_contract`, `search_docs`.
- The example library becomes an evolvable artifact in its own right (prune, re-score,
  merge) — the system curating its own experience.

## Integration points

- `evolution/orchestrator.go`: `buildSeed` (inject retrieved examples), the commit path
  (`captureExample` on green), `buildExamples` (becomes the fallback when the store is
  thin).
- `evolution/sieve.go`: `RunSieve` stays; add `RunAgenticSieve` alongside.
- `inference/client.go`: add `InvokeTools` (+ the `tools`/`tool_calls` request/response
  types). Reuse the retry/reconnect + `Observe` plumbing.
- `compiler`: `compile_check` calls the existing `CompilerService.CompileGenotype` — no
  new compiler work, just exposure as a tool.
- Ledger: `urn:hdm:examples:*` refs; GC-reachable so the store persists.
- Observability: `/perf` (or a new `/examples`) lists the library; `[EXAMPLE]` logs a
  capture; the agentic loop logs its tool calls.

## Verification

- Unit: store round-trip; novelty gate (dup not captured, better-scoring wins);
  retrieval ranking (a `render` target gets `render` examples, position-draw ranks top
  for a draw-at-field intent); `InvokeTools` loop with a scripted fake model
  (tool_call → result → final) — no network.
- `go build ./... && go vet ./... && go test ./...` green each phase.
- End-to-end: re-run the **single bouncing ball** on the local model. Phase 1 target:
  the renderer passes its position-coordination checks (draws at the ball's coords);
  physics improves. Phase 2 target: `compile_check` eliminates the ABI-violation stalls
  and the app converges to a visibly moving, bouncing ball.

## Key decisions / risks

- **Retrieval signal**: start keyword + kind + shape (dependency-free, house style);
  add embeddings (`/v1/embeddings`) only if recall is poor. No new third-party deps.
- **Curation**: capture *novel* green cells, not all — keep the library high-signal or
  retrieval degrades. Bounded per kind/tag.
- **Tools inform, never bypass, verification.** Whatever the agent produces still runs
  the full compile → gauntlet → chaos → commit gates. The example store is untrusted
  guidance, not a shortcut.
- **Client-side loop** keeps determinism/rollback/security in Go and is portable across
  providers; the cost is we implement the (small, well-understood) tool-use state machine
  ourselves rather than delegating to a server agent.
- **Cost**: agentic loops multiply model calls. Gate Phase 2 to *hard/stalled* cells (via
  the existing escalation ladder) and bound `maxSteps`, so cheap cells stay one-shot.

# Design spec: a knowledge base (examples + docs) + client-side agentic synthesis

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

1. A **growable, two-tier knowledge base** in the ledger, both tiers seeded and then
   **added to by the system itself**:
   - **Examples** — concrete *worked WAT solutions*. The "here is code that passes."
     Captured from the system's own green cells (and a few hand-written seeds).
   - **Documents** — descriptive prose on *how to build architectural concepts*: the
     shared-memory map, the draw-stream format, the wall-bounce pattern, composition /
     combinators, private paging, and so on. The "here is how/why to approach this
     class of problem." Authored (seeded from the ABI/architecture knowledge that today
     only lives in `evolution.Capabilities` and hard-coded prompts) and refined over
     time — by the operator and, later, by the system distilling its own lessons.

   Examples are *episodic* (a specific solution); documents are *semantic* (a reusable
   concept). Synthesis benefits from both — the doc explains the pattern, the example
   shows it working — and both are the evolvable-surface philosophy applied to synthesis
   experience.
2. **Client-side agentic synthesis**: turn the sieve from a one-shot completion into a
   small tool-using agent that HDM orchestrates **in Go**. The model can call tools over
   *both* tiers — `find_examples` / `find_docs` / `inspect_example` / `read_doc` — and
   (the sleeper hit) `compile_check` — and HDM executes them locally and feeds the
   results back until the model returns a final cell.

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

### Documentation store (`evolution` or the `examples`/`knowledge` package)

A `Document` is a piece of reusable, conceptual knowledge — prose, not code:

```
Document {
  ID         string   // content hash
  Title      string   // "Reading a contract field and drawing at it"
  Topic      string   // short slug: "draw-at-position", "wall-bounce", "memory-map"
  Kinds      []CellKind // which cell kinds it is relevant to (empty = all)
  Body       string   // markdown: the concept, the ABI facts, the pattern, pitfalls
  SeeAlso    []string // ids of related examples / documents
  Provenance string   // "seed" | operator | "distilled from <cell/objective>"
}
```

Stored under `urn:hdm:docs:*`, same discipline as the example store. Where an example
is *episodic* (one solution), a document is *semantic* (a pattern that spans many).

**Seeding — architectural how-tos.** Author a starter set from knowledge that today is
scattered across `evolution.Capabilities` (the ABI/memory-map/draw-stream block injected
into every prompt) and the prompts themselves — now first-class, retrievable, and
editable:
- **the shared-memory map** (regions, the sandbox at `0xB0000`, the HMI register);
- **the draw-stream format** (24-byte records, layer/op, drawing a circle at `(x,y)`);
- **read a contract field and act on it** (the pattern the ball's renderer missed);
- **integrate + reflect at a boundary** (the wall-bounce pattern);
- **composition / combinators**, **private paging**, **the WAT ABI + assembler
  constraints** (one table, i32 vs i64 — the rules the coder kept breaking).

**Authoring / growth.** Documents are written, not captured from cells:
- **operator-authored** (a `POST /docs` surface, mirroring the existing feedback path)
  — the human supplies architectural guidance;
- **system-distilled** (Phase 3): when a pattern recurs across several green cells, a
  model pass summarizes it into a document, adversarially checked before it is stored.
Curate the same way — one strong doc per topic beats many thin ones.

### Retrieval

`findExamples(kind, semantics, reads, writes, k)` and the parallel
`findDocs(kind, semantics/topic, k)` both rank their store by:
1. **kind match** (a `render` target only sees `render` examples) — hard filter;
2. **shape overlap** (shared contract-field roles / same entry / similar port count);
3. **semantic overlap** (keyword/tag overlap for Phase 1; embeddings later via omlx
   `/v1/embeddings`).

Retrieval returns the top-K worked **examples** and the top-K relevant **documents**,
injected into `buildSeed` **in place of** the static `buildExamples` — the doc explaining
the pattern above the example demonstrating it (or alongside, when a store is thin).

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
  the example + document stores + the compiler) and runs `InvokeTools`, then feeds the model's final
  WAT through the same compile/verify gates as today. The acceptance gauntlet is
  unchanged: **tools inform synthesis; they never bypass verification.**

### The tools (all pure, all Go, all read-only except the returned WAT)

Examples (episodic):
- `list_examples(kind?)` → ids + one-line semantics of available examples.
- `find_examples(query, kind?, k?)` → ranked worked examples (semantics + WAT + score).
- `inspect_example(id)` → the full WAT of one example.

Documents (semantic):
- `list_docs(kind?)` → titles + topics of available documents.
- `find_docs(query, kind?, k?)` → ranked documents (title + body).
- `read_doc(id)` → the full document body.

Compiler / context:
- `compile_check(wat)` → runs the candidate through **our** WAT assembler
  (`compiler.CompileGenotype`) and returns `ok` or the exact error
  ("at most one table", "i32.div_s type mismatch: expected i32 got i64", …). This is
  what would have let Qwen-Coder self-correct the ABI violations it kept making.
- (optional) `contract_fields()` → the target app's shared-state field names/offsets, so
  a renderer reads the *right* address.

## Phasing

Each phase builds + tests green and is independently useful.

### Phase 1 — stores + retrieval-augmented synthesis (no tool-calling)
- **Example** store (types, `SaveExample`/`LoadExamples`, content-addressed) and
  **Document** store (`SaveDocument`/`LoadDocuments`), same discipline.
- Seed the simple canonical **examples** (+ register the hand-written cells) and the
  starter **documents** (memory map, draw-stream, draw-at-position, wall-bounce, WAT
  ABI/assembler rules).
- `captureExample` on green (with the novelty gate), wired at the commit/convergence
  point. (Documents are authored, not captured, this phase.)
- `findExamples` + `findDocs` (kind + shape + keyword/tag ranking).
- Inject retrieved docs + examples into `buildSeed`, replacing the static few-shot.
- **This alone should move the single-ball renderer** — a "draw at position" doc *and* a
  worked example are exactly the missing guidance. Verify by re-running the ball.

### Phase 2 — client-side agentic tool loop
- `inference.InvokeTools` (OpenAI `tools` protocol, Go-orchestrated loop, graceful
  fallback). Probe which transport omlx honors most reliably (`chat/completions` tools).
- The full tool set above (example + document + compiler tools), backed by the stores +
  compiler.
- `RunAgenticSieve`; route hard/stalled cells through it (compose with the existing
  cost-driven escalation — escalate to *agentic + reasoner* on stall).
- Operator document authoring (`POST /docs`), so a human can add architectural guidance
  the agent will retrieve.
- Verify: a cell that fails one-shot converges agentically; `compile_check` visibly
  cuts ABI-violation dead-ends; a `find_docs` call surfaces the relevant pattern.

### Phase 3 — the knowledge base curates itself (optional, later)
- More tools as they prove out: `inspect_cell(urn)`, `list_contract`.
- **System-distilled documents**: when a pattern recurs across several green cells, a
  model pass summarizes it into a `Document` (adversarially checked before storing) —
  the system writing its own how-tos.
- Both stores become evolvable artifacts (prune, re-score, merge) — the system curating
  its own experience, episodic *and* semantic.

## Integration points

- `evolution/orchestrator.go`: `buildSeed` (inject retrieved docs + examples), the commit
  path (`captureExample` on green), `buildExamples` (becomes the fallback when the stores
  are thin). Much of the starter document set is the ABI/memory-map prose already in
  `evolution.Capabilities` — lifted into the ledger and made retrievable.
- `evolution/sieve.go`: `RunSieve` stays; add `RunAgenticSieve` alongside.
- `inference/client.go`: add `InvokeTools` (+ the `tools`/`tool_calls` request/response
  types). Reuse the retry/reconnect + `Observe` plumbing.
- `compiler`: `compile_check` calls the existing `CompilerService.CompileGenotype` — no
  new compiler work, just exposure as a tool.
- Ledger: `urn:hdm:examples:*` and `urn:hdm:docs:*` refs; GC-reachable so both stores
  persist.
- `integration/gateway_ui.go` + `main.go`: a `POST /docs` operator-authoring surface
  (Phase 2) and a read `/knowledge` panel listing examples + documents.
- Observability: `[EXAMPLE]` / `[DOC]` logs on capture/author; the agentic loop logs its
  tool calls.

## Verification

- Unit: example + document store round-trips; novelty gate (dup not captured,
  better-scoring wins); retrieval ranking (a `render` target gets `render` examples, and
  a "draw at a field" intent ranks the draw-at-position doc + example top); `InvokeTools`
  loop with a scripted fake model (tool_call → result → final) — no network.
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
  the full compile → gauntlet → chaos → commit gates. The knowledge base — examples *and*
  documents — is untrusted guidance, not a shortcut; a wrong doc can only mislead a
  candidate that verification then rejects.
- **Client-side loop** keeps determinism/rollback/security in Go and is portable across
  providers; the cost is we implement the (small, well-understood) tool-use state machine
  ourselves rather than delegating to a server agent.
- **Cost**: agentic loops multiply model calls. Gate Phase 2 to *hard/stalled* cells (via
  the existing escalation ladder) and bound `maxSteps`, so cheap cells stay one-shot.

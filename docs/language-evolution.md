# Evolving the system through a language change

Status: design (exploration). How a change to Flux itself (grammar/semantics/lowering)
propagates and is accepted. Companion to [self-hosting-flux.md](self-hosting-flux.md)
(§0.1 north star) and [grammar-constrained-flux.md](grammar-constrained-flux.md). The
dependency tree here (§1) is one instance of the general derivation graph in
[lineage.md](lineage.md) — a language change is the extreme case of a lineage rebuild.

## 0. The problem

Every other kind of change touches one cell. A **language change touches everything
written in the language at once** — every cell genome, every worked example, the
grammar, and every prompt that shows Flux. So a language evolution can't be judged
by one cell's acceptance; it is judged by whether **the entire stack rewrites and
runs green under the new language.** That is both the difficulty and, it turns out,
the clean acceptance gate.

## 1. The dependency tree

```
        L  = the language  (front-end: grammar def, checker, lowering)
        │
   ┌────┼───────────────┬────────────────┐
   ▼    ▼               ▼                ▼
   G    X               C_i              (S_i)
 grammar  examples     cell genomes    scenarios
 (GBNF)   (Flux)       (Flux)          (BEHAVIOR)
   │        │
   └───►  P  ◄──┘
        prompts  (template + inlined X + G)
```

- **G (grammar / GBNF)** is *derived* from L — automatic.
- **X (worked examples)** and **C_i (cell genomes)** are *instances* of L — Flux text
  that must be valid under L.
- **P (prompts)** show Flux: they inline examples X and the grammar G.
- **S_i (scenarios)** assert *behavior* — and behavior is **language-independent**.

That last point is the hinge (§2).

## 2. The invariant: behavior is language-independent

A cell's scenarios say what it must *do* (bounce, draw at a position, forward a
slider) — never how the Flux is spelled. So across a language change **the scenarios
don't move.** They are the fixed contract the new expression must still satisfy.

This makes the acceptance gate concrete and sound: a language evolution is accepted
iff **every cell, re-expressed in L', still passes its unchanged scenarios** (and the
north-star fitness — tokens/convergence/validity — improves in aggregate). Behavior
is the invariant; the language is the evolvable *expression* of it.

## 3. Acceptance = the whole stack, rebuilt green, atomically

A language change ΔL is treated as a **mutation whose test is the whole stack**, run
under the existing verify→commit→rollback discipline but at the language level, as an
**epoch** (HDM already has epoch checkpoints):

1. Snapshot the current epoch (all of X, C_i, P, G, L).
2. Apply ΔL; migrate the tree (§4–5); re-run every cell's scenarios.
3. **Promote** the epoch iff every cell is green under L' and the aggregate fitness
   improved; otherwise **roll back** to the snapshot — atomic, reversible.

So "accept a language change" literally means "the whole system came back green in
the new language, and cheaper/more-reliably to generate."

## 4. Trace order — and the chicken-and-egg

The migration walks the DAG topologically:

```
L'  →  G' (derive grammar)  →  X' (migrate examples)  →  P' (rebuild prompts: template + X' + G')  →  C_i' (migrate genomes)  →  re-run S_i  →  promote|rollback
```

But there's a cycle: you can't *author* cells in L' without prompts P' (which need
examples X'), and you can't get green cells to *harvest* into X' without authoring
in L'. Break it with **seed examples**: a small hand-authored (or mechanically
migrated) set of L' examples primes X', which primes P', which lets cells migrate;
their greens then *replace* the seeds in X' (the existing `captureGreens` path). The
seeds are the bootstrap that makes the cycle a spiral.

## 5. Migrating the instances (X and C_i)

Two regimes, cheapest first:

- **Additive / backward-compatible ΔL** (a new feature; old Flux still valid — e.g.
  the reads/writes clauses becoming *optional/derived*). No migration: old instances
  stay valid; accept iff the new capability works and nothing regresses.
- **Breaking ΔL** (old Flux no longer valid — a renamed primitive, changed syntax).
  Each instance must be rewritten to L':
  - **mechanical transform** when the change is a syntactic rewrite (strip a clause,
    rename a token) — cheap, deterministic, preferred;
  - **re-evolve from scenarios** when it isn't — regrow the cell in L' against its
    *unchanged* scenarios (§2). Expensive but general: it is exactly "rewrite and run
    the whole stack," and it is sound because the behavior contract is preserved.

Prefer mechanical where possible; fall back to re-evolution per cell that fails to
transform. This bounds cost to the cells a change genuinely breaks.

## 6. Single source of Flux truth — prompts never embed Flux

The migration surface collapses if **Flux lives in exactly one place** — the artifact
store (examples X + genomes C_i) — and everything else is *derived or inlined*:

- **Prompts are templates with example slots**, not Flux-bearing strings. At build
  time the *current* examples (from X, in the current language) and the *current*
  grammar (G) are **lazily inlined**. So when ΔL migrates X and regenerates G, every
  prompt updates for free — no prompt is ever hand-edited for a language change.
- **The grammar G is derived** from L (already true: `flux.GBNF`).

This is the load-bearing principle for §3–4: because P is *assembled* from X and G,
migrating X (and deriving G) is the *whole* prompt migration.

**Current gap:** `fluxSeedBlock` (evolution/flux_bridge.go) hard-codes worked Flux
examples in the seed string, and `knowledge_seed.go` holds Flux constants. That is
embedded Flux in the prompt/code — it would have to be hand-edited on every language
change. The fix (the first concrete step): make the seed block pull its worked
examples from the KB example store (which the orchestrator already retrieves via
`FindExamples`), so the *only* Flux in a prompt is lazily inlined from X.

## 7. Versioning and atomicity

- **Stamp every instance with a language version.** The migrator walks
  not-yet-migrated artifacts in dependency order (§4); a partially-migrated store is
  never live.
- **The whole migration is one epoch.** Promote or roll back atomically (§3), reusing
  the existing epoch-checkpoint machinery. A language change is thus as safe as any
  other evolutionary step: worst case it reverts.

## 8. First concrete step

De-embed Flux from prompts: move `fluxSeedBlock`'s hard-coded worked examples into
the KB (seed them as `Example`s) and have the seed block **lazily inline** them from
X. This makes the *single-source + lazy-inlining* principle (§6) real, which is the
prerequisite for treating a language change as a store migration (§4) rather than a
hunt through the codebase for embedded Flux. Small, and it pays off immediately:
capturing a better green cell already improves the examples the prompt shows.

## 10. Exploration results (live, Qwen3.6-27B-oQ4)

The two-tier exploration loop (cheap scoreboard → epoch gate) is built and run. The
scoreboard (`flux/scoreboard.go`) measures a variant on the LLM-optimal axes; the
proposer (`flux/proposer.go`) hands the model the board and lets it propose the next
variant. Findings so far (temp 0.7, small N — noisy, directional):

- **Grammar guarantees syntax, not types.** Syntax-valid rate is ~1.0 by
  construction; the parse+**type-check** rate sits lower (≈0.75–1.0). That gap is the
  type-error rate the grammar admits — a concrete language-evolution target (make a
  class of type errors ungrammatical and the gap closes).
- **Canonicality is the weak axis** (≈0.20–0.30): at temp>0 the model scatters across
  many equivalent programs. But **fixing write ORDER did not fix it** — the
  `canonical` variant moved canonicality within noise while costing tokens. The
  scatter lives in the **expression trees and let-bindings**, not write ordering. The
  next canonicality lever should target those (e.g. constrain/most-canonicalize let
  usage), not field order.
- **The LLM proposer reasons correctly about its own generation.** Shown the board,
  the model proposed `clauses=true canonical_writes=true`, explicitly "targeting
  canonicality … while maintaining clauses=true to preserve the valid-rate floor that
  dropped in the terse variant." Sound reasoning — and the cheap tier still
  **rejected** the proposal on measurement (0.68 ≤ baseline 0.90), without paying for
  a whole-stack epoch. Proposer + filter working as designed.

Net: baseline (clauses, free writes) remains the fittest measured language; terse and
canonical are recorded negatives; the open lever is expression/let canonicality.

## 9. Implementation status

The mechanism is built; see [lineage.md §9](lineage.md) for the full map. In short:
the de-embed first step landed (Flux worked examples live in the KB, tagged by
language, lazily inlined — `fluxSeedBlock` embeds no Flux program); the dependency
tree is recorded generically as lineage; and this doc's acceptance gate (§3) is
`evolution.ProposeLanguageChange` — snapshot the affected refs, rebuild the stale
stack under a new ledger-backed language version via the memoized driver, promote iff
all green + fitness improved, else roll back atomically. The remaining seam is the
real `reauthor` adapter (re-author a cell against its unchanged scenarios) and an
actual better-language front-end to promote.

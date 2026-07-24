# Flux as the language — genome, completeness, and the standard library

**Status:** design / direction · **Depends on:** [functional-ir.md](functional-ir.md)

The direction: **Flux becomes the only language cells are authored, stored, and
evolved in. WAT stops being a genome and becomes an internal compilation target,
hidden from authors.** Getting there is three things — make Flux the *genome*, make
the language *complete*, and build a *standard library* whose few irreducible
primitives drop to WAT as vetted `unsafe`/foreign functions. This doc lays out what
that looks like and where the gaps are.

## 0. The immediate gap: we store WAT, not Flux

Today the genome is WAT. `Repository.PutCell(urn, watSource, bytecode, …)` persists
the **lowered WAT** (`sieve.WAT`) as the genotype; the Flux source the model authored
is only ever in `SieveOutcome.Raw` and is thrown away. Consequences:

- **Solver iterations restart.** `buildSeed` receives the stored WAT as the current
  genotype; in Flux mode we suppress it, but we don't persist or re-show the prior
  **Flux**, so each synthesis re-derives the program from the spec instead of
  refining its last draft. The iterative `flux_check`/`flux_run` loop only iterates
  *within* one synthesis call, never *across* frames.
- **Mutation/optimization operate on WAT text**, not the Flux AST — losing the whole
  "every mutation is well-formed and type-correct by construction" win
  (functional-ir.md §8).

**P0 (do this first, independent of the larger vision): make Flux the stored
genome.** Persist the Flux source as the genotype; lower to WAT/bytecode on demand
for execution and verification (the phenotype). Show the prior Flux in `buildSeed`
("CURRENT PROGRAM — improve it"). This is a small, high-value change that makes
cross-frame refinement real.

### What P0 touches
- `SieveOutcome` carries `Flux` alongside `WAT`; `RunAgenticSieve`/`runSieve` set it.
- Commit path: `PutCell` stores the **Flux** as genotype, the lowered **bytecode** as
  phenotype (the repo already stores both — only the genotype's *language* changes).
  A small tag records the genotype language (`flux` vs `wat`) so mixed cells coexist
  during migration.
- `buildSeed`: render the prior Flux as the current program to improve.
- Any "recompile the stored genotype" path becomes language-aware (lower Flux vs
  assemble WAT) — most reads use the stored bytecode directly, so this is narrow.
- Gauntlet, tapes, acceptance, contracts, MVCC commit: **unchanged** — they operate
  on the lowered bytecode, which is identical either way.

## 1. Flux as the evolvable genome (P1)

Once the genome is Flux *text*, the next step is Flux *AST* as the mutation substrate:
structural edits (subtree replacement with a type-compatible subtree, constant
tweaks, operator swaps within a type class), typed crossover, and fusion/memoization
as sound rewrites. Every offspring is valid by construction — no more syntax-dead
mutants, which is the larger half of the payoff. The WAT lowering stays the
deterministic bridge to execution.

## 2. Removing the escape hatch (the hard part)

Going **Flux-only** means every cell — including the system cells (`sys:map`,
`sys:dict`, the cognition cells that call the LLM, the dispatchers) — must be
expressible without hand-written WAT in the app surface. That needs (a) a *complete*
language and (b) a place for the few things that genuinely can't be pure Flux. The
escape hatch doesn't vanish — it **moves**: from "any cell may be raw WAT" to "only
the standard library may contain vetted `unsafe` WAT primitives, and app authors
never see them."

## 3. Completeness gaps — what v1 can't express, and how to close each

| Capability | v1 today | Gap → how to close |
|---|---|---|
| **Iteration** | none (no loop/recursion) | Needed to scan arrays, draw N entities, run collision over a set. → bounded `map`/`fold` over a sequence, and/or a `(repeat n …)` / tail-recursive `define`. Bounded so fuel stays predictable. |
| **Sequences / arrays** | none; contract `i32[40]` fields are skipped | → a `[T]` type with index/slice/length. Unifies combinator vectors and terminal streams (functional-ir.md). Lowers to base+stride addressing. |
| **Float** | `Float` typed but not lowered | → finish f32 lowering + float prims (`+.`/`*.` or overload by operand type) for sub-pixel motion, ratios, trig-lite. |
| **Char / String** | none | → `Char` (roadmap) + string literals for the monadic terminal cell and any text UI. Lower to byte sequences. |
| **User functions / reuse** | none (only `let`) | → `(define (name params…) body)` so logic can be factored and a stdlib can be written *in Flux*. Inlined or lowered as real wasm funcs. |
| **Effects / host calls** | none (pure reads→writes/draw) | The big one. Cells that call the kernel — LLM cognition (`cognitive-engine`), `invoke-cell` dispatch, `cell-logger`, `block-storage`, `chronos` (time), paging — can't be pure. → a typed **capability/effect API**: `(reason prompt)`, `(invoke urn arg)`, `(log …)`, `(now)`, `(rand)`, each a typed Flux primitive backed by a host import. Purity is preserved *within* a tick by threading the effect result as a value. |
| **Higher-order / combinators** | none | → first-class function references so a cell can pass a leaf to `map`/`fold`; this is how the existing `sys:*` combinators become native. |
| **Randomness / time** | none | → `(rand)` / `(now)` effect prims (chronos). Needed for spawns, jitter, procedural content. |
| **Multi-cell / topology** | one cell = one entry | Fracture/fusion already operate at the orchestrator level; Flux stays single-entry per cell, which is fine. |

None of these are blockers to *starting* — they're the roadmap to *retiring* WAT.

## 4. The standard library

A layer of reusable, typed operations the app author calls but never implements:

- **Math/geometry:** `lerp`, `dist`, `sign`, vector add/scale, `wrap`, `approach`.
- **Collision/spatial:** AABB overlap, circle hit, clamp-to-bounds.
- **Collections:** `map`/`fold`/`filter`/`find` over `[T]` (the combinators, as library).
- **Drawing helpers:** draw a sprite at a position, a grid, a bar, a number.
- **Input helpers:** "was this key pressed this tick" (the `hmi_event_seq` compare
  idiom), pointer-in-rect.
- **Random/time:** seeded RNG, frame time.

Most of these are **written in Flux itself** (once functions + sequences exist). The
handful that can't be — host calls, raw memory/bit tricks, anything needing a wasm
construct Flux doesn't expose — are the `unsafe` layer below.

## 5. `unsafe` / foreign — where WAT survives, hidden

A Flux function may be **backed by hand-written, verified WAT** with a typed Flux
signature:

```lisp
(foreign (rand : () -> Int) "call $host_rand")        ; a capability
(foreign (popcount : (Int) -> Int) "(i32.popcnt …)")   ; a bit trick Flux lacks
```

- The **signature is Flux-typed**; the **body is opaque WAT**, compiled and
  fuel-costed like any WAT, verified once, then trusted.
- `foreign` is the *only* place WAT appears. App cells call `rand` or `popcount` as
  ordinary typed functions — they never see, write, or evolve WAT. This is exactly
  "drop to WAT as the unsafe equivalent": the stdlib and the kernel-capability
  bindings are built from `foreign` primitives; everything above is pure Flux.
- It also **preserves the open substrate.** WAT was chosen as maximally expressive;
  `foreign` keeps that escape valve for the rare primitive, while making the common
  path typed, safe, and evolvable. The openness moves into a vetted, named registry
  instead of being sprayed across every cell.

## 6. Phasing

- **P0 — Flux is the genome.** Store Flux source; lower on demand; show prior Flux in
  the seed. Cross-frame refinement works. *(Small; do now.)*
- **P1 — Flux AST is the evolvable genome.** Mutation/crossover on the tree;
  fusion/memoization as rewrites.
- **P2 — Language completeness.** Sequences + iteration + float + `define`.
- **P3 — Capabilities + `foreign`.** The effect API and the WAT-backed primitive
  registry; begin the standard library.
- **P4 — Retire the app-surface escape hatch.** Migrate the system cells to Flux (or
  `foreign` primitives); remove raw-WAT authoring. WAT is now internal only — authors
  see only Flux + the stdlib.

The escape hatch (raw-WAT cells) **stays available until P4** so nothing regresses
during the transition; it is removed only once the stdlib + `foreign` cover every
cell the system needs.

## 7. Risks & open questions

- **Coverage during transition.** Until the stdlib is broad, some cells (cognition,
  exotic system cells) live in WAT. Keeping the tagged mixed-genome model (Flux *or*
  WAT per cell) lets both coexist without a flag day.
- **Fuel & verification of `foreign`.** Each foreign body must be fuel-costed and
  verified once; its typed signature must be enforced at the call boundary so a
  mistyped foreign can't corrupt the type system.
- **Effect purity.** The capability API must thread effect results as values so a
  tick stays deterministically replayable (the gauntlet/tapes depend on it). Effects
  that mutate the world (LLM calls) already have the `EffectfulImports` machinery —
  the Flux binding must respect it (an effect can't be silently dropped by a mutation).
- **Performance.** Lowered Flux must stay competitive with hand-WAT for hot cells;
  the lowerer should produce tight code (and `foreign` is the pressure valve for the
  rare hot primitive).
- **Determinism of the lowerer.** It already is; keep it so, since the genome→phenotype
  map must be reproducible for caching and verification.

## 8. Recommendation

Do **P0 now** — it's small, it fixes a real correctness gap (solver iterations should
refine stored Flux, not restart), and it's a prerequisite for everything else. Treat
P1–P4 as the staged path to Flux-only, with the escape hatch retired last. The
`foreign`/stdlib design is what makes "hide WAT entirely" reachable without giving up
the open substrate that made WAT attractive in the first place.

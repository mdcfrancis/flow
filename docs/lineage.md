# Lineage — the derivation graph as a byproduct of the forward process

Status: design (exploration). The generic mechanism beneath
[language-evolution.md](language-evolution.md): dependency tracking is not a special
artifact built for a language change — it is **lineage recorded as every cell is
derived**, and a change (of any input) becomes a **memoized, incremental rebuild**
over that graph. Companion to [self-hosting-flux.md](self-hosting-flux.md) (§0.1 north
star) and [grammar-constrained-flux.md](grammar-constrained-flux.md).

## 0. The reframe

Language-evolution (that doc) treats a language change as "rewrite and run the whole
stack," and builds a dependency tree *for that purpose*. But the dependency tree is
not special to language changes — it is the general fact that **every cell was
derived from inputs**: a language, a grammar, a prompt template, some retrieved
examples, a set of scenarios, maybe a parent it fractured from. If we **record those
input hashes as part of the forward process**, then *any* input change — a better
example, a changed contract field, a new language — is the same operation:

> rebuild the transitive closure of what depended on the thing that changed, and
> reuse everything that didn't.

A language change is then not a bespoke migration; it is the **extreme case** of this
one operation — the change whose transitive closure happens to be the whole stack.

This is a content-addressed incremental build graph (Nix / Bazel / Salsa). HDM
already has the two load-bearing pieces; the missing part is the edges.

## 1. What HDM already gives us (and why the cycle is already broken)

- **Content addressing.** The CAS ledger already hashes every artifact — genotype,
  phenotype, descriptor. So every input already has a stable content ID, and "did
  input X change?" is a hash comparison, not a diff.
- **Memoization.** The runtime already skips recompute when inputs are unchanged
  (frame-level read-set memo; dispatch-level return-value memo keyed on arg bytes;
  async verdict cache). That is *exactly* the cache-hit rule an incremental rebuild
  needs — today applied to **execution**; the proposal is to apply the same rule to
  **derivation**.

The consequence: **memoization breaks the cycle.** The scary loop — the language is
built by cells, which are authored *using* the language — terminates for the same
reason a Nix build terminates: an unchanged, already-resident node is a cache hit,
and **a cache hit is the base case of the recursion.** When the self-hosted compiler
cells are in the ledger and their input-hashes haven't moved, the rebuild does not
recurse into re-deriving the language from itself; it stops at the memo table. A
self-hosted language is therefore not a paradox — it is a fixpoint the memo discipline
already knows how to reach. The cycle-breaker exists; it just isn't used for
authoring yet.

**The genuinely missing part is narrow: the lineage edges** — *what each artifact's
inputs were* — recorded so the rebuild can walk them. That is the whole proposal.

## 2. Authoring memo ≠ execution memo (the one real subtlety)

Today HDM memoizes *running* a cell. Authoring memo means: **don't re-synthesize a
cell whose derivation-inputs are unchanged.** Same content-hash principle, different
table — and one subtlety that shapes the design:

> LLM authoring is **nondeterministic**. A cache hit cannot mean "re-run the model
> and expect the same bytes." It must mean "**reuse the stored genotype.**"

So lineage is not merely an *index* of dependencies — it **stores the result**
alongside its input-hashes, so a hit can return it. It is the derivation cache, not a
side-table describing one. (This is also why re-authoring is only forced when an
input hash actually moves — otherwise a nondeterministic re-run would churn the whole
stack on every rebuild.)

## 3. The lineage record

For every derived artifact (a cell genome; an example; a prompt build), record the
result together with the content-hashes of the inputs it was derived from:

```
Lineage(cellURN @ genotypeHash) = {
  result:     <the derived artifact itself — genotype bytes / example / prompt>
  language:   <hash of the language front-end / version it was authored under>
  grammar:    <hash of the GBNF it was decoded under>
  prompt:     <hash of the prompt TEMPLATE (not the inlined Flux — that's `examples`)>
  examples:   [<hashes of the worked examples a tool call retrieved during authoring>]
  scenarios:  <hash of the acceptance suite it was verified against>
  contract:   <hash of the app's shared-state contract>
  parent:     <genotypeHash it fractured from, if any>
  leaves:     [<URNs it dispatches, if a combinator>]
  model:      <model id + decode params>
}
```

- Keyed by the *result* hash, so the graph is immutable and append-only — the natural
  shape for CAS, and it **lives next to the descriptor in the ledger**, not in a side
  file that can drift.
- `scenarios` is recorded but is language-INDEPENDENT (language-evolution.md §2): a
  language change does not move the scenario hash, so a cell's behavioral contract is
  a stable node the rebuild re-verifies against.

## 4. Recording lineage IS the forward process (incl. agentic recursion)

The edges are emitted where they are created — no separate bookkeeping pass:

- **authoring a cell** (`RunSieveWithLayout` / `RunAgenticSieve`) records `language`,
  `grammar`, `prompt`, `model`, and — this is the agentic-recursion part — **every
  example a tool call retrieved** (`find_examples` / `inspect_example` results) as
  `examples` edges. The agentic loop's recursion *is* the derivation; each tool call
  that pulls a dependency is an edge.
- **fracture** records `parent → children`.
- **capture-green** records `cell → example` (a green cell promoted into the KB is a
  new node whose input is the cell).
- **cell-dispatch / combinators** record `combinator → leaves`.
- **scenario / contract authoring** records the `scenarios` / `contract` hashes the
  cell was built against.

So lineage accrues for ALL cells as a side effect of the loop already running — the
"track dependencies as part of the forward process for all cells" you asked for.

## 5. Rebuild = memoized DAG evaluation

A change to any input node triggers:

```
rebuild(target):
  inputs = lineage(target)
  for each input i: rebuild(i)                      # recurse bottom-up
  if hash(all inputs) == cachedInputHash(target):   # early cutoff
     return cached result(target)                    #  → cache hit: reuse (breaks cycles)
  recompute(target)                                  #  → re-author / re-derive
  re-verify(target) against its (unchanged) scenarios
  record new lineage(target)
```

- **Incremental**: only the transitive closure of the changed input recomputes;
  everything else is a cache hit. A better *example* re-runs just the cells that used
  it; a changed *contract field* re-runs just its readers/writers; a *language*
  change re-runs (transitively) everything — the extreme case, not a special case.
- **Early cutoff** (Salsa/Adapton): if a recomputed input produces the *same* hash
  (e.g. a language change a cell's spelling is invariant to), its dependents are NOT
  rebuilt. Cheap changes stay cheap.
- **Termination**: cache hits on unchanged, already-resident nodes are the base cases;
  the L→cells→L cycle bottoms out at the memo table (§1). Re-authoring fires only
  when an input hash actually moved (§2), so a rebuild never churns unchanged nodes.

## 6. Language change, restated as a rebuild

ΔL changes the `language` hash → invalidate the transitive closure of nodes with a
`language` edge (examples, genomes, and the derived grammar/prompts) → memoized
rebuild §5 → run the whole affected subtree → **promote the epoch iff every re-run
cell is green against its unchanged scenarios and aggregate fitness improved, else
roll back** (language-evolution.md §3). Same engine, at the language level.

## 7. Plan

1. **Lineage record + store** — a `Lineage` node keyed by result hash in the ledger,
   holding the result + input hashes (§3); `RecordLineage(result, inputs)` /
   `LineageOf(hash)`.
2. **Emit edges in the forward process** — instrument the sieve/agentic authoring
   (language, grammar, prompt, model, retrieved examples), fracture
   (parent→children), capture (cell→example), dispatch (combinator→leaves), and the
   scenario/contract hashes. Best-effort, append-only.
3. **Memoized rebuild driver** — `Rebuild(target)` per §5, keyed on input hashes,
   reusing the CAS + the existing memo discipline; early-cutoff on unchanged hashes;
   cache hit returns the stored result (§2).
4. **Wire the epoch gate** — a change enqueues a rebuild; promote/rollback atomically
   (language-evolution.md §3, §7).

## 8. First concrete step

Start **recording lineage** at cell authoring — even before any rebuild driver
exists — because the graph must accrue from the forward process to be there when a
change needs it, and it immediately buys provenance ("what was this cell derived
from?") plus the data every later phase needs. Begin with the cheap, high-value edges
the authoring path already has in hand: `language` / `grammar` / `prompt` / `model`
and the `examples` retrieved via the agentic tools.

This composes with the language-evolution first step (lazy-inline examples from the
KB): once prompts are assembled from the example store, the `examples` a cell was
authored with are exactly the nodes to record — **lineage and lazy-inlining are the
same edge seen from two directions.**

## 9. Implementation status

Landed (all of §7, plus the language-change gate):

- **Store** — `evolution/lineage.go`: the `Lineage` record, `RecordLineage`,
  `LineageOf` (provenance), `FindLineageByInputs` (the authoring memo), and
  `InputsHash` (the order-independent content address of a derivation).
- **Edges in the forward process** — `evolution/orchestrator.go`: `buildSeed`
  assembles the input edges (language/grammar/prompt/model/examples/scenarios/
  contract) and the commit chokepoint attaches the genotype hash and records them.
  De-embedding Flux from prompts made the `examples` edge real (worked examples are
  lazily inlined from the KB, so the edge is the set of stored examples retrieved).
  **Verified live**: a `bounce:physics` cell authored in Flux recorded
  `lang=flux/v1`, a real grammar hash, and the two KB Flux examples it drew from.
- **Rebuild** — `evolution/rebuild.go`: `PlanRebuild` (hits vs stale) + `Execute`
  (reuse hits, early-cutoff on the memo, else re-derive via an injected callback,
  memoizing only green results).
- **Epoch gate** — `evolution/language_epoch.go`: `ProposeLanguageChange` snapshots
  the affected refs, rebuilds under the new ledger-backed language version, and
  promotes iff all green + fitness improved, else rolls back atomically.

Open seam: the real `reauthor` adapter that drives the orchestrator to re-author a
cell against its unchanged scenarios (the callback the driver and gate inject) — and
the actual better-language front-end whose promotion the gate would accept, which is
the open research the north star (docs/self-hosting-flux.md §0.1) frames.

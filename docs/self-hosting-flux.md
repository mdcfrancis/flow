# Self-hosting Flux — the compiler as an evolvable artifact

Status: plan (exploration). Companion to [functional-ir.md](functional-ir.md),
[flux-as-the-language.md](flux-as-the-language.md), and
[flux-interfaces.md](flux-interfaces.md).

## 0. Goal and the decisions that scope it

Move the Flux **front end** (parse → check → lower) out of the trusted Go core and
into **Flux cells**, so the evolutionary loop can evolve *the language itself* — new
primitives, better lowering, optimizations — as ordinary cell evolution, gated by
the same verify → gauntlet → rollback machinery. The outer Go collapses toward what
[[hdm-evolvable-surface]] already wants: **security, rollback, and a bootstrap seed.**

Two decisions fix the scope:
1. **Front-end-only.** The WAT→WASM **assembler (`compiler/sieve.go`, ~2.3k loc)
   stays in Go** — not just to save work, but as a **validation gate** (§5.1): the
   Flux front end emits WAT *text*, and the trusted Go assembler validates it before
   assembling. So the evolving compiler's output is checked at the Go layer on every
   compile. The language's design space is in parse/check/lower, not in WASM
   byte-encoding (a frozen spec, not worth evolving).
2. **Plan the complete set.** We lay out the *entire* language-completeness
   prerequisite up front (§3–4), rather than discovering it feature-by-feature.

## 1. The compile path, and what moves

```
source ──[ FRONT END: parse → check → lower ]──► WAT text ──[ assembler ]──► WASM ──wazero──► run
          flux/*.go  (~1k loc Go)  ── MOVES TO FLUX          compiler/sieve.go  ── STAYS GO
```

`wazero` and the host ABI (memory, draw, HMI) are the machine — they never move.
The compiler-as-a-cell is a `[Char] → [Char]` program: **source text in, WAT text
out** — i.e. it implements the `terminal` interface from flux-interfaces.md.

## 2. The crux: Flux must become a real language first

Flux v1 has `Int/Bool/Color`, pure functions over a *fixed* set of shared-state
fields, and nothing else — no strings, no lists, no recursion, no user types, no
heap. **You cannot write a compiler in it.** A parser needs source *text*; a tree
walker needs *recursion*; an AST and a symbol table need *dynamic, arbitrary-size
structures*. So the honest shape of this project is:

> **Self-hosting Flux ≈ growing Flux into a small ML** (ADTs, pattern matching,
> recursion, functions, strings, lists, an arena heap), *then* rewriting the
> compiler in it.

The relocation (Arc B) is mechanical; the language lift (Arc A) is the bulk.

## 3. The complete prerequisite set (language completeness)

Everything below lowers to WAT (the assembler is unchanged). Grouped, in
dependency order:

**Text**
- `Char` (a byte) and `[Char]`/`String` (a heap byte-array): literals, `length`,
  index, slice, concat, `char->int`/`int->char`, classification (`is-digit`,
  `is-alpha`, `is-space`). — reads source, emits WAT.

**Data**
- **Sequences `[T]`** — `nil`/`cons`/`head`/`tail`/`index`/`len`/`map`/`fold`.
  Token streams, AST child lists, assoc-list symbol tables.
- **Records (product types)** — `(record Prim (op Int) (args [Node]))` with named
  fields; construction + field access. AST nodes, `Field{type,offset}`.
- **Variants (sum types) + `match`** — `(data Node (IntLit Int) (Var String) (Prim …) …)`
  with exhaustive `match`. An AST node / a token is one-of-N — this is the
  representation core.
- **Maps / dict** — name→type, field→offset. Start as assoc-lists over `[T]`; expose
  the runtime `dict` (execution/dict.go) later if hot.

**Memory** (§4)
- An **arena heap** in the cell's private page; boxed values; the lowering ABI the
  data types sit on.

**Control**
- **Functions** — intra-cell `(define (parse-expr toks) …)`; a compiler is dozens of
  them. Lower to multiple WASM funcs per cell.
- **Recursion** — recursive descent (parse) and tree walks (check/lower). Bounded by
  a fuel/depth cap that fits HDM's fuel model.

**Effects / ABI**
- **`Result`/error** — the checker returns positioned errors (`Ok a | Err pos msg`).
- **String I/O** — the `[Char] → [Char]` cell ABI: a source-text input channel, a
  WAT-text output channel. This is the `terminal` interface realized.

## 4. The memory model — the crux substrate

The one genuinely new runtime piece. Everything dynamic (strings, lists, records,
variants, AST) is a **boxed, heap-allocated value**: a header word (tag + size)
followed by fields/elements, addressed by an **offset into the cell's private
page** (see [[hdm-private-pages]]).

- **Arena allocation, reset per compile.** A bump pointer at a fixed page offset;
  `alloc(n)` advances it. **No GC** — a compilation is a bounded arena that is reset
  wholesale at the start of each compile. This is the decisive simplification: a
  compiler is a pure `arena → arena` transform, so freeing is "reset the pointer."
- **The Flux author never sees pointers.** ADTs/lists/strings lower to box
  constructors and `match` lowers to tag-dispatch + field loads; the lowerer owns
  the layout. `(cons x xs)` → `alloc` a 2-cell + store; `(match n (Prim op args) …)`
  → load the tag, `br_table`, bind fields.
- **Load/store of structured records** and a pointer-sized value are the only new
  primitives the lowerer emits beyond today's flat field access.

This is a standard ML-to-WASM lowering; the arena-per-compile discipline keeps it
GC-free and fuel-friendly.

## 5. Bootstrap chain and the trust anchor

```
stage 0 (seed):  Go front end  compiles  compiler-in-Flux source  ─►  WASM compiler cell
stage 1:         WASM cell     compiles  a Flux program           ─►  WAT ─(Go assembler)─► WASM
self-host:       WASM cell     compiles  its OWN Flux source      ─►  byte-identical WASM   ⇒ fixpoint
```

**Differential verification is the trust anchor.** Throughout Arc B, the Go
implementation is the *oracle*: for every input `x` and stage `S`,
`FluxS(x) == GoS(x)` (WAT text for `lower`; a canonical serialization of the typed
AST for `check`; the token/AST stream for `parse`). The corpus is every existing
Flux test cell **plus the compiler's own source**. Self-hosting is reached — and
*proven* — when the Flux compiler compiles its own source to a WASM cell
byte-identical to what Go produces.

### 5.1 Two validation layers at the Go boundary

Differential verification proves *correctness during migration* (the Flux stage
matches Go). A second, independent guarantee provides *safety during evolution*:
the Flux front end's output is **untrusted WAT text**, and the **trusted Go
assembler validates it on every compile**. An evolving or mutated lowerer can emit
only WAT the assembler accepts — malformed WAT is rejected at the Go boundary and
never becomes WASM or runs. wazero validates the assembled WASM as a further check,
and the existing verify/gauntlet/chaos gates then validate *behavior*. So the trust
stack is layered and entirely at the Go layer:

```
Flux front end (untrusted, evolvable)  ─►  WAT text
    │  Go assembler:  validates WAT structure/types, rejects malformed  ← safety
    │  wazero:        validates WASM                                     ← safety
    │  gauntlet/chaos: validates behavior vs the regression corpus      ← correctness
    ▼
committed only if all pass
```

This is *why* the assembler stays Go (decision 1): it is the checkpoint that makes
evolving the compiler safe — a broken lowerer degrades to a rejected compile, never
a bad execution.

## 6. The trusted core (what stays Go, forever)

- **The assembler** (`compiler/sieve.go`): WAT→WASM. Fixed target *and* the
  validation gate on the evolving compiler's output (§5.1) — the reason it stays Go.
- **wazero + the host ABI/kernel**: memory, draw, HMI, fuel.
- **Security/rollback**: sandbox, enforced masks, MVCC commit, epoch checkpoints, the
  verify/gauntlet/chaos gates. Evolving the *compiler* makes these MORE load-bearing
  — a mutated lowerer that miscompiles must be caught and reverted.
- **The bootstrap seed**: a pinned Go build that can compile the Flux compiler source
  from scratch, checkpointed, so the language is always rebuildable from source even
  after it has self-hosted and evolved.

## 7. End-to-end phases

**Phase 0 — Foundations (memory model).** Arena allocator in the private page;
boxed-value representation (header tag+size); the lowerer heap ABI (`alloc`, box,
structured load/store). No new surface syntax yet. *Verify:* a hand-written cell
that conses a list and folds it runs correctly and resets its arena.

**Phase 1 — Language completeness in Go (Arc A).** Extend the Go parse/check/lower
to the full §3 set, in dependency order: (1a) Char/String, (1b) `[T]`, (1c) records,
(1d) variants + `match`, (1e) functions + bounded recursion, (1f) `Result`, (1g) the
`[Char]→[Char]` cell ABI. Each increment ships with unit + differential tests and a
worked cell. This is the bulk — Flux becomes a small ML.

**Phase 2 — Rewrite the front end in Flux (Arc B),** stage by stage, each verified
differentially against its Go twin on the corpus:
- **2a. `lower`** (typed AST → WAT) — most mechanical, least text; do first.
- **2b. `check`** (AST → typed AST) — symbol table + type rules.
- **2c. `parse`** (text → AST) — recursive descent over `[Char]`; hardest; do last.

**Phase 3 — Self-host fixpoint + cutover.** The Flux compiler compiles its own
source to a byte-identical WASM cell. Flip the live compile path to the Flux
compiler cells; demote the Go front end to the bootstrap seed (§6).

**Phase 4 — Evolve the language.** The compiler is now cells: the evolutionary loop
can mutate/optimize lowering and add primitives, gated by differential + gauntlet +
rollback. The payoff — a living language.

## 8. The Flux-zero first spike (before committing to Arc A)

Prove the *mechanism* on a toy before paying for full completeness. **Flux-zero** =
the smallest sublanguage whose own `lower` is writable in Flux-zero: arithmetic +
`let` + one `run-tick`, lowering to WAT. Demonstrate:
1. Go lowers `lower-in-Fluxzero` → a WASM lowerer cell.
2. That cell lowers a Flux-zero program → WAT → (Go assembler) → WASM → runs.
3. It lowers **its own source** → identical WASM (the fixpoint).

This validates the bootstrap loop + differential harness end-to-end, and — crucially
— surfaces exactly which §3 features the compiler-in-Flux demands first, so Arc A is
driven by a real consumer rather than guessed.

## 9. Risks and open questions

- **Scope.** Arc A is "build a small ML compiler." Real months of work; the plan's
  value is sequencing it behind a proof (§8) and a differential oracle (§5).
- **Performance.** A Flux-hosted parser over text in a fueled sandbox: rely on
  arena-reset, fuel budgets, and frame memoization; profile `parse` early.
- **Recursion vs. fuel.** Deep ASTs against a depth/fuel cap — bound recursion,
  provide an explicit-stack iterative form for the hot walk if needed.
- **Determinism of the fixpoint.** Byte-identical self-compilation requires
  deterministic lowering (stable ordering of everything the lowerer emits) — worth
  auditing the Go lowerer for nondeterminism (map iteration order, etc.) before
  porting.
- **`match` exhaustiveness + errors** in Flux — the checker checks Flux; when the
  checker is itself Flux, its own type errors must be as legible as Go's are today.

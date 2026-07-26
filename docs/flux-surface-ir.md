# Flux: evolvable surface, invariant IR

Status: design + first increment landed. The architectural split that makes the
LANGUAGE safely evolvable — the enabler for the science loop
([language-evolution.md](language-evolution.md), [lineage.md](lineage.md)).

## The split

```
surface text  ──Read──▶  *Cell (the IR)  ──Lower──▶  WAT  ──▶ WASM
  EVOLVABLE                 INVARIANT          THE ONLY GO INVARIANT
 (grammar +               (typed, checked     (hides the ABI boilerplate:
  parser +                 form — the input    memory import, entry export,
  renderer)                to lowering)        param handling, addressing)
```

Three layers, one invariant:

- **Surface** — the concrete syntax the model generates. Grammar + parser (read) +
  renderer (write). *Evolvable.* This is what the science loop optimizes for LLM
  efficiency (tokens to generate, canonicality, tokens to represent in context).
- **IR** — `flux.Cell`: the typed, checked cell (name, kind, reads/writes, a typed
  expression tree). *Invariant.* It is deliberately close to WAT — its only job is to
  be lowered.
- **Backend** — `Lower(*Cell, Layout) → WAT`. The sole thing the Go layer must hold
  invariant. It hides the ABI boilerplate that motivated Flux over hand-written WAT.
  **It never needs the grammar.**

## Why this is the enabler

Because behavior is determined *entirely* by `Lower(*Cell)`, and `Lower` never sees
the surface:

> Any surface that reads to an equivalent `Cell` lowers to the same WAT and therefore
> preserves external behavior **by construction.**

So a language change — a different, more LLM-efficient surface — is safe as long as it
lands on the same IR. The parser moves *out of the invariant path*: it is a swappable
`Surface`, not fixed Go the way a change would have to edit. The Go invariant shrinks
to exactly `Lower`.

This turns the science loop's behavior-invariant gate from an expensive whole-stack
scenario re-run into a **cheap, structural check**: compare `BehaviorHash` (the hash
of the lowered WAT). "Did this surface change preserve behavior?" is a hash
comparison. Rigor at the gate is what buys freedom at the hypothesis.

## The seam (implemented)

`flux/surface.go`:

- `Surface` — a first-class `{ Read(text)→*Cell; Render(*Cell)→text; Name() }` pair.
  The default is `SExpr` (today's syntax: `Read` = `Parse`+`Check`, `Render` is the
  inverse). A language experiment is a *different `Surface` over the same IR*.
- `DefaultSurface` — what the operational system uses; `Compile` is now exactly
  `DefaultSurface.Read → Lower`.
- `BehaviorHash(*Cell, Layout)` / `SameBehavior` — the WAT-identity of a cell; the
  structural invariant gate.

`flux/render.go`: `Render(*Cell)` — the inverse of `Parse`+`Check`, the missing half
that makes the surface a true *serialization* of the IR (text → IR → text), and the
way the system SHOWS the model a cell (examples, the current program) — the
"processed by the LLM" half of the efficiency goal.

Verified: for view, compute-with-`let`, and nested-expression programs, `text → IR →
text → IR` lowers to identical WAT (`TestSurfaceRoundTripPreservesBehavior`), and
`Render` is idempotent.

## First A/B result (live, Qwen3.6-27B-oQ4, 5 samples/task)

| surface | syntax | valid | tokens | canonicality | fitness |
|---------|:------:|:-----:|:------:|:------------:|:-------:|
| sexpr   | 1.00   | 1.00  | 112.2  | 0.40         | 0.976   |
| forth   | 0.80   | 0.60  | **23.3** | **0.62**   | 0.866   |

The concatenative surface generates in **~5× fewer tokens** and is **substantially
more canonical** (0.62 vs 0.40 — the model converges on one program far more often).
Both are behavior-safe by `BehaviorHash`. The cost is **validity**: type-valid rate
falls to 0.60 (grammar-valid-but-mis-stacked programs), which — because valid-rate is
the floor — leaves net fitness just behind s-expr.

So the surface war is not settled; it is *localized*. Forth wins big on the axes that
matter (tokens, canonicality) and the whole gap is validity — exactly what the
**prologue-locals / depth-bound** lever targets (`forthMaxDepth`, `=:` bindings).

### Depth lever (second crank)

Tightening the stack-depth bound 3→2 (forcing more `=:` intermediates) + a sharpened
stack-effect dictionary:

| surface   | syntax | valid | tokens | canonicality | fitness |
|-----------|:------:|:-----:|:------:|:------------:|:-------:|
| sexpr     | 1.00   | 1.00  | 111.5  | 0.33         | 0.944   |
| forth-d2  | **1.00** | **0.53** | **17.8** | 0.57     | 0.781   |
| forth-d3  | 0.80   | 0.33  | 17.6   | 0.60         | 0.598   |

The bound moved the predicted axes: at d2 **syntax reaches 1.00** (every stack
balances) and validity ~doubles vs d3 (0.53 vs 0.33). Forth's efficiency wins are
robust across runs (~6× fewer tokens, canonicality ~0.57–0.62 vs ~0.33–0.40).

**Residual (robust across three runs):** Forth's type-valid rate stays below 1.0
while s-expr sits at 1.0 under an *equally permissive* grammar — so the gap is not the
grammar but the model's weaker **type-tracking in postfix** (adding a comparison
result, mixing a color into arithmetic). Depth helps because shallow stacks are
easier to keep well-typed, but it can't close it alone. The next levers are (a) more
samples to de-noise the estimate, then (b) a **type-stratified** Forth grammar
(separate int / bool / color expressions so type errors become ungrammatical) or
richer worked examples for familiarity. s-expr still leads on fitness because
valid-rate is the floor; Forth's 6× token + canonicality edge makes closing validity
the prize.

### Type-stratification (third crank — the win)

Making the residual error class ungrammatical: `GBNFForthTyped` segregates iexpr /
bexpr / color and binds to typed local pools (`=: i0` Int, `=: b0` Bool), writes take
only an iexpr into an Int field.

| surface           | syntax | valid | tokens | canonicality | fitness |
|-------------------|:------:|:-----:|:------:|:------------:|:-------:|
| **forth-typed**   | 1.00   | **1.00** | 41.9 | **0.67**    | **1.250** |
| sexpr             | 1.00   | 0.93  | 110.4  | 0.28         | 0.854   |
| forth-d2 (untyped)| 0.93   | 0.60  | 22.0   | 0.42         | 0.768   |

**The type-stratified Forth surface overtakes s-expr decisively.** Validity goes to
1.0 (type errors are now ungrammatical, exactly as the s-expr GBNF is
valid-by-construction — applied to the failing axis), canonicality to 0.67, tokens
~2.6× leaner than s-expr. Fitness 1.25 vs 0.85. Behavior identical by `BehaviorHash`.

This is the **first measured better-LLM language**: a surface the model generates
more reliably, more canonically, and far more cheaply than the human-oriented
S-expression, for identical external behavior. It is a genuine ΔL for the epoch gate
to promote.

**De-noised (12 samples/task):** forth-typed fitness **1.184** vs sexpr **0.870** —
validity parity (0.94 each), ~2.6× fewer tokens, ~2.3× more canonical. The 5-sample
1.0 validity was optimistic; the residual ~6% is unbound-local references (the
grammar admits `i2` as an atom even if never bound), the exact class the agentic
`ForthDiagnose` feedback loop closes. The win is confirmed and robust.

Note: the agentic `ForthDiagnose` tool (explicit stack/word vs type feedback for
model iteration) is built and ready, but the grammar lever alone reached valid=1.0 —
so the agentic loop is now insurance for the harder residual (behavioral correctness,
rare unbound-local cases), not required for validity.

### Wired operationally (HDM_FLUX_SURFACE=forth) — verified live

The winning surface is now selectable in the real synthesis path: the sieve (one-shot
AND agentic) authors cells in type-stratified Forth, validated by the agentic
`ForthDiagnose` loop, and the derived vocabulary (neg/abs/min/max/clamp) is distilled
into an evolvable prologue so only the minimal core reaches the backend. A live grow
committed this `bounce:physics` genome — stored as Forth, the evolved language:

```
ball_x ball_vx + =: i0
ball_y ball_vy + =: i1
i0 0 < i0 screen_width >= or =: b0
i1 0 < i1 screen_height >= or =: b1
b0 ball_vx neg ball_vx ? =: i2
b1 ball_vy neg ball_vy ? =: i3
i0 0 screen_width clamp -> ball_x
i1 0 screen_height clamp -> ball_y
i2 -> ball_vx
i3 -> ball_vy
```

Type-stratified locals (i-pool / b-pool), shallow prologue-bound stacks, words for
neg/clamp/?, `-> field` writes — the science-loop-evolved language, running in the
system for identical external behavior.

## Where this goes

- **Now**: the surface is a swappable `Surface` in Go; a language experiment is a new
  `Surface`, gated by `BehaviorHash`. The science loop optimizes the surface with a
  structural invariant.
- **Ledger-resident prologue (landed)**: the derived vocabulary
  (neg/abs/min/max/clamp and any evolved words) lives in the ledger as data
  (`InstallPrologue`), and the checker consults its arities — so the system can
  rewrite its own vocabulary from data with no Go change (proven: adding `double`).
- **Surface promotion (landed)**: the default authoring surface is promotable ledger
  state. `PromoteSurface` runs a whole-stack epoch — re-author every cell in the new
  surface, promote iff all green, else roll back — so flipping the default to forth is
  a verified language change, not a flag.
- **Parser-as-data (landed, S-expr family)**: a surface can now be a `SurfaceSpec`
  (data: a keyword lexicon + clause policy) that a single generic `SpecSurface` engine
  interprets — Read remaps skin heads back to canonical then type-checks, Render emits
  the skin. Specs are ledger-resident (`SaveSurfaceSpec`/`LoadSurfaceSpec`), so a new
  S-expr-family surface is data the system holds and can evolve, no Go type — the
  analog of the ledger-resident prologue. Proven behavior-safe by `BehaviorHash`.
  (Skin tokens must be valid Flux identifiers; `DeriveClauses` is behavior-equivalent
  but re-orders reads in the WAT, so it is not `BehaviorHash`-identical.)
- **Buffer capability (landed) — parser/stream cells are now Flux**: Flux gained a
  `TBuffer` type and bounded indexed memory — `(at buf i)` / `(len buf)` reads and
  `(store buf i v)` writes, every access **clamped to `[0, len)`** so a buffer field
  is as isolation-safe as a scalar one. This closes the gap the substrate map found
  (the parser substrate — `sys:stream-fold` + `sys:list` + `invoke-cell` — existed,
  but Flux couldn't walk a buffer). **Verified in the sandbox**: a tokenizer authored
  in Flux (`(at src cursor)` → classify → `(store dst cursor …)` → advance) tokenizes
  a buffer over ticks. Parser/stream cells no longer need hand-WAT — they are Flux.
- **A full parser, in Flux (landed)**: on the buffer capability, a complete parser is
  now authored entirely in Flux — no hand-WAT. Two cells compose:
  a **tokenizer** (bytes → tokens; multi-digit numbers accumulated and flushed on a
  boundary, operators emitted, spaces skipped via self-preserving stores) and a
  **shift-reduce engine** (tokens → result over a stack: SHIFT a number, REDUCE on an
  operator). Driven per-tick by the frame loop, the pipeline parses+evaluates
  arbitrary multi-digit RPN arithmetic (all four operators, spaces, nesting), verified
  in the sandbox. Parser/stream cells are Flux components now, the archetype for
  buffer-processing cells generally.
- **Self-hosted surface parser (landed, postfix surface)**: a Flux cell now parses
  the surface into IR STRUCTURE, not just a value. Same shift-reduce shape, but it
  ALLOCATES AST nodes — a number becomes a leaf `(0, value, ·)`, an operator pops two
  handles and becomes an inner node `(op, left, right)` — in a `nodes` buffer with a
  bump allocator, pushing handles on the stack. Chained after the tokenizer,
  `text → tokens → AST node buffer`, and Go reconstructs the tree from the buffer.
  **The parse — surface text into a structured AST — is done by a cell, not by Go.**
  Verified in the sandbox on multi-digit RPN arithmetic. This is full self-hosting of
  the parse step: the system parses (a surface of) its own language with a cell it can
  evolve; Go only deserializes the tree and lowers it. `Lower` (IR→WAT) remains the
  sole fixed invariant.
- **Remaining frontier**: extend the node grammar from arithmetic to full cell forms
  (write/draw/let terminals, field refs) and adopt the nested S-expression surface
  (a parse-state stack), to make the self-hosted parser the operational reader. The
  shift-reduce-to-AST mechanism above is the whole engine; what's left is grammar
  breadth.
- **The efficiency goal decomposes cleanly onto the surface**: *generated* efficiently
  (grammar the model decodes under — the scoreboard) and *processed* efficiently
  (`Render` compactness/legibility). Both are surface properties; behavior is IR.

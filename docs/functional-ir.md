# Functional IR — a typed functional core the model authors, lowered to WAT

**Status:** draft / design spec · **Working name:** *Flux* (provisional) · **Branch:** `feat/functional-ir`

## 1. Why

We asked the LLM to be two things at once: a **logician** (`pos += vel`; reflect at
the wall; clamp) and a **stack-machine assembler** (i32/i64 discipline, operand
order, locals-at-top, one-table, balanced parens, 24-byte record layout, `0xB0000`
load/store arithmetic). It is failing at the assembler role, and that role is almost
entirely deterministic.

Measured, one fresh single-ball grow on local gemma (`HDM_TAXONOMY=1`, see
`evolution/taxonomy.go`), 38 WAT generations:

```
compile{ empty:11  structural:10  stack-sig:5  other:5  ok:7 }   accept{ stall:3 }
```

- **82% (31/38) never produced compiling, signature-valid WAT.** All 31 were app
  cells (0 system-cell compile failures).
- The failures are boilerplate: `local.get requires one immediate` (locals),
  `unexpected end of input inside group` (parens), `too many results` (stack),
  `i64` vs `i32` (width).
- Even the tool-assisted agentic path (with `compile_check` in the loop) produced
  only malformed WAT — 10 final failures, all structural/stack, **zero logic**.
- **Genuine logic (acceptance stalls): 3.** ~9% of failures. The model rarely
  reaches the stage where its reasoning even matters.

**Thesis:** raise the abstraction the model writes at. Let it author the *logic* in a
small typed functional language; a deterministic Go **lowerer** owns types, stack,
structure, and addressing and emits guaranteed-valid WAT. The output stays WAT — the
genome, runtime, sieve, and knowledge base are unchanged — but the entire
compile-stage failure class becomes impossible by construction.

## 2. It fits what already exists

HDM is already functional under the hood; WAT is a stack-machine leaf language bolted
under a functional/dataflow architecture:

- Cells are monadic pure functions: `run-tick : (i32,i32)->i32`,
  `render-frame : (i32,i32)->i32` over a shared-memory store.
- `EvolvableLeaf` is "a tiny pure function cell"; `sys:map`/`fold`/`filter`
  combinators dispatch leaves *as functions* (`appgen/combinators.go`).
- `RunFusion` composes cells; frame-level **memoization** skips a cell "whose inputs
  are unchanged" — both rely on purity.
- The **plan layer** already declares each cell as `reads: […]  writes: […]` and
  frames the view as "a pure function of the physics state."

A functional IR is not a new paradigm bolted on — it is naming the paradigm the
system already runs on, and giving the model a surface that matches it.

## 3. The computational model: a cell is a pure function over typed channels

A cell is a **pure function**. What varies between kinds of cell is only the **typed
I/O channel** the runtime binds to it. The cell performs no mutation and no I/O
itself; the runtime reads the input channel, calls the function, and applies the
output channel. Purity lives inside the cell; effects live at the boundary.

The channel is what makes each cell *shape*:

- **Compute cell** (`run-tick`): `Record(reads) → Record(writes)` over the shared
  store. Fields not written are unchanged.
- **View cell** (`render-frame`): `Record(reads) → DrawList`. Writes nothing.
- **Terminal cell** (`terminal-io`, roadmap): `[Char] → [Char]` — a pure function
  over a character stream, stdin to stdout. A stateful REPL variant combines a
  shared-state channel with the char channel: `(Record(reads), [Char]) →
  (Record(writes), [Char])`. This is the classic monadic `interact :: (String →
  String)` shape, and it is the reason the type system carries a distinct `Char`
  from day one (§4.3) — so a keystroke can never be silently added to a pixel.
- **Effectful cell** (cognition: LLM host calls, `invoke-cell` dispatch): a minority.
  v1 leaves these as raw WAT (see §9 escape hatch); a later effect-primitive model
  can bring them in.

The unifying idea: **one calculus of pure functions; the runtime wires a typed
channel to each.** Compute, view, and terminal are the same thing over different
channels — which is why they share a grammar, a type system, and a lowerer, and why
adding a channel (a terminal, a socket, an audio buffer) is a runtime-binding + type
question, not a new language. This is *not* full monadic IO — each cell is a pure
function over a typed value, which is exactly what these cells already are.

## 4. The language

Syntax is **S-expressions** — the exact parenthesized surface the model already emits
for WAT. The difference is **semantics**: a Flux expression *evaluates to a value*
(tree semantics), with no stack to balance and no declaration order to get wrong.
Every structural/stack failure we measured is stack-machine semantics leaking through
tree syntax; Flux keeps the syntax and removes the trap.

### 4.1 The bouncing-ball cells, in Flux

Physics — the cell that failed 31 times as raw WAT:

```lisp
(cell physics
  (reads  ball_x ball_y vel_x vel_y screen_w screen_h)
  (writes ball_x ball_y vel_x vel_y)
  (let ([nx (+ ball_x vel_x)]
        [ny (+ ball_y vel_y)]
        [bounce_x (or (< nx 0) (>= nx screen_w))]
        [bounce_y (or (< ny 0) (>= ny screen_h))])
    (write
      (vel_x (if bounce_x (neg vel_x) vel_x))
      (vel_y (if bounce_y (neg vel_y) vel_y))
      (ball_x (clamp nx 0 (- screen_w 1)))
      (ball_y (clamp ny 0 (- screen_h 1))))))
```

Renderer:

```lisp
(cell renderer
  (reads ball_x ball_y)
  (draw
    (circle ball_x ball_y 8 #xFFCC33FF)))
```

That is the whole logic. Everything else — module wrapper, memory import, `run-tick`
export + signature, locals declaration and placement, the `0xB0000+` load/store for
each field, the 24-byte draw-record encoding, stack ordering, and width/type
discipline — is the lowerer's job, and it cannot get any of them wrong.

### 4.2 Grammar (v1, sketch)

```
cell   := (cell NAME (reads ID*) (writes ID*)? body)
body   := (write binding*)          ; compute cell → output record
        | (draw  prim*)             ; view cell   → draw list
        | (stdout expr)             ; terminal cell → [Char]  (roadmap)
binding:= (ID expr)
lit    := INT | FLOAT | CHAR | BOOL | COLOR   ; 42  3.14  'a'  true  #xFFCC33FF
expr   := lit | ID                  ; ID is a field read or a let-bound name
        | (let ([ID expr]*) expr)
        | (if expr expr expr)       ; guard : Bool; branches same type
        | (OP expr*)                ; typed primitive application
prim   := (rect x y w h color) | (line x1 y1 x2 y2 color) | (circle cx cy r color)
```

The terminal cell shape (roadmap) reuses the same expression language over the
`[Char]` channel — one calculus, a different bound channel:

```lisp
(cell echo               ; [Char] -> [Char], stdin to stdout
  (stdin in)
  (stdout (map to-upper in)))
```

### 4.3 Types

A real type system from the start — not because the bouncing ball needs it, but
because the type system is what keeps *domains* apart while everything lowers to
WAT's handful of numeric types. A keystroke, a pixel coordinate, a velocity, and a
truth value are all `i32` in wasm; conflating them is a bug, and the whole point of a
typed IR is to make that bug unrepresentable.

**Base types (v1):**

| Flux type | lowers to | literals | notes |
|---|---|---|---|
| `Int`   | i32 | `42`, `-1`        | pixel/index/scalar arithmetic |
| `Float` | f32 | `3.14`, `0.5`     | sub-pixel motion, ratios |
| `Char`  | i32 (code point) | `'a'`, `'\n'` | terminal I/O; distinct from `Int` by design |
| `Bool`  | i32 (0/1) | `true`, `false` | result of comparisons; guards `if` |
| `Color` | i32 (RGBA) | `#xFFCC33FF`   | draw-record color; distinct from `Int` |

**On the roadmap:** sized ints (`I64`), `Float64`, and the parametric **sequence**
`[T]` — one type that serves both the combinators (`[Int]`, `map`/`fold`) and the
terminal channel (`[Char]`, stdin/stdout). Records are the field environment; a
`DrawList` is `[Prim]`.

**Where types come from — declared, not guessed.** Field types come from the
shared-state **contract** (each contract field already declares a type); `reads`
brings typed fields into scope and `writes` must produce each field's declared type.
Literals carry their type. Primitives are typed, with arithmetic/comparison
**overloaded** over `Int` and `Float` but resolved by operand type — **no implicit
coercion**: crossing `Int`↔`Float` or `Char`↔`Int` needs an explicit `to-float` /
`to-int` / `char->int` / `int->char`, so widths are never silently mixed (the old
`i64`-into-`i32.div_s` bug is unrepresentable). Checking is **bidirectional** —
declared field/literal/primitive types flow through `let` and `if` (branches must
agree) — so no annotations are needed inside a cell and no full Hindley–Milner
inference is required.

A name not in `reads`/`let`, a `write` to a field of the wrong type, or an unresolved
overload is a **semantic type error** caught before lowering and fed back to the
model as a sentence ("`vel_x` is `Float`, but you wrote an `Int`") — not a stack
trace.

### 4.4 Primitives (v1)

Arithmetic `+ - * / mod neg` (`Int`/`Float`), comparison `< <= > >= = !=` (→`Bool`),
boolean `and or not`, control `if`/`let`, numeric `min max clamp abs`, conversions
`to-float to-int char->int int->char`, output `write`, and draw constructors
`rect line circle` taking a `Color`. Field reads are bare identifiers resolved against
`reads`/`let`.

Higher-order `map`/`fold` over a `[T]` (combinator cells; terminal streams) lands with
the sequence type on the roadmap.

## 5. Lowering to WAT — what the lowerer owns

`Flux AST → typecheck → WAT` is deterministic. The lowerer owns, and therefore
guarantees, every deterministic class we measured:

| Failure class (measured) | Owned by the lowerer |
|---|---|
| `structural` (module, import, export, locals, parens) | emits the wrapper + `run-tick`/`render-frame` export + locals-at-top |
| `stack-sig` (`too many results`, stack order) | allocates locals and orders the stack from the expression tree |
| `type` (i32/i64) | typed IR; no implicit coercion, so widths are never mixed |
| `addressing` (`0xB0000+` load/store) | resolves each field name to its offset; emits load on read, store on write |
| draw-record ABI (24-byte layout) | `circle`/`rect`/`line` → the record stream + returns the byte length |
| `empty` (no code) | see §10 — grammar-constrained decoding makes "not a program" unrepresentable |

The model can no longer emit any of these errors because it never writes the encoding
— it writes an expression.

## 6. Fuel & determinism

Evaluation is **strict** (eager). Laziness would make the Hamiltonian's per-op fuel
non-deterministic; strictness keeps fuel a pure function of the AST + inputs. Each
Flux primitive has a fixed lowering to WAT ops, so the existing fuel accounting and
the gauntlet/tape machinery carry over unchanged — the lowered WAT is graded exactly
as today.

## 7. Combinators become native

The `sys:map`/`fold`/`filter` cells and `RunFusion` are today special cells reached
by dispatch. In Flux they are language constructs, and their laws are rewrite rules:
`(map f (map g xs)) → (map (compose f g) xs)` is a sound fusion; a pure cell whose
`reads` are unchanged returns cached `writes` (memoization) — provable from purity,
not enforced by a checker.

## 8. Genome & evolution — the biggest win

Today the genome is **WAT text**, and mutation/crossover produce mostly *invalid
text* — a share of the 82% is bad mutants, not just bad first drafts. Make the genome
the **typed Flux AST**:

- Every mutation (subtree replacement with a type-compatible subtree, constant tweak,
  operator swap within a type class) and every crossover is **well-formed and
  type-correct by construction**. The evolutionary search stops discarding offspring
  on syntax-dead ends.
- **Fusion and memoization become sound AST rewrites**, not risky text transforms —
  the functional laws are the rewrite rules.

This upgrades the *evolution* half of the system, which is the whole point — not just
first-shot synthesis.

## 9. Escape hatch — WAT stays

Flux is the front door for the common case (numeric state machines + draw lists), not
a ban. A cell may still be authored and stored as raw WAT for the rare computation the
core doesn't cover, and for effectful cognition cells in v1. The runtime handles both;
the genome carries a tag for which substrate a cell uses.

## 10. Killing `empty` without JSON

`empty` (model returned prose / no `(module …)`) was 29% of failures — an
instruction-following failure, not a logic one. We do **not** solve it by wrapping
the language in JSON: Flux **is** S-expressions, both the surface the model writes and
the canonical genome. Three levers keep the empty class down while staying in S-expr:

1. **A smaller, more natural surface.** Flux is a fraction of WAT's size and reads
   like the logic itself; the model emits it far more readily than a stack-machine
   module. Much of `empty` was the model balking at WAT's ceremony.
2. **Grammar-constrained decoding.** The Flux grammar is tiny and context-free, so it
   can be handed to the sampler as a GBNF/BNF constraint (supported by llama.cpp and
   several MLX servers). The decoder then *cannot* emit a non-program — every token
   stays on a valid Flux path. This makes "not a program" unrepresentable without any
   JSON, and also removes residual `structural` (paren) faults at the source.
3. **The same feedback loop, but semantic.** When constrained decoding isn't
   available, a parse error feeds back like today's correction directive — but the
   message is a sentence about the grammar, not a stack trace, and the retry surface
   is small.

The canonical genome serialization is Flux S-expression text (readable, model-native,
diffable) — see §8.

## 11. Integration with the existing pipeline

- **Plan → Flux:** the plan's `Steps` ("if `nx >= screen_w` then `vel_x = -vel_x`;
  clamp") map almost directly onto the expression. `AuthorPlan` could emit Flux
  directly, making plan → IR → WAT one pipeline where the model only ever writes
  logic.
- **Sieve:** `RunSieve` gains a Flux path — model emits Flux, `parse → typecheck →
  lower → WAT`, then the **same** downstream (signature check, acceptance, gauntlet,
  commit). A parse/type error feeds back like a compile error, but these are rare and
  the messages are semantic.
- **Knowledge base:** examples become Flux snippets (simpler than WAT); the
  `draw-at-position` / `wall-bounce` docs become Flux patterns.
- **Taxonomy:** re-run with the Flux path; expect `structural`/`stack-sig`/`type`/
  `empty` to collapse toward zero, leaving `accept:*` (logic) as the residue — the
  stage we actually want the model working at.

## 12. Rollout

- **Phase 0 — spike (this branch):** define the v1 grammar + the `Int`/`Float`/
  `Bool`/`Color` core (enough for physics + a view; `Char`/`[T]` are declared in the
  type system but exercised later with the terminal); hand-write parser + typechecker
  + lowerer for `run-tick`/`render-frame`; unit-test that Flux → WAT validates through
  wazero and behaves. Prove the bouncing-ball physics + renderer **converge on gemma
  via Flux** where raw WAT failed 31×. Decisive and cheap.
- **Phase 1 — synthesis path:** wire Flux into `RunSieve` behind `HDM_IR=1`; model
  emits Flux; raw WAT remains fallback/escape hatch.
- **Phase 2 — genome:** Flux AST as the evolvable genome; mutation/crossover on the
  tree; fusion/memoization as AST rewrites.
- **Phase 3 — combinators & corpus:** `map`/`fold` native; migrate examples/docs.

## 13. Decided / open

**Decided (this iteration):**
- **Surface & genome are S-expression Flux — no JSON.** `empty` is handled by the
  smaller surface + grammar-constrained decoding + semantic feedback (§10).
- **A real type system from v1** — `Int`/`Float`/`Char`/`Bool`/`Color`, no implicit
  coercion (§4.3). `Char` is distinct from day one to make the monadic terminal
  (`[Char] → [Char]`) type-check later.

**Open:**
1. **`Float` representation:** f32 vs fixed-point `Int` for sub-pixel motion — the
   fuel/determinism trade. Both are typeable; which is the default for physics?
2. **Sequence type `[T]`:** the shared abstraction behind combinators (`[Int]`) and
   the terminal channel (`[Char]`). What are its bounds (fixed capacity? runtime
   length?) so lowering and fuel stay predictable?
3. **Terminal channel binding:** how the runtime wires stdin/stdout buffers to a
   `terminal-io` cell, and whether a REPL cell composes the char channel with a
   shared-state channel in one cell or two.
4. **Effect model:** bring cognition cells in via explicit effect primitives
   (`(reason prompt)`, `(invoke urn args)`) or keep them as WAT indefinitely?
5. **Higher-order scope:** `map`/`fold` over `[T]` only, or first-class lambdas?
   Bounds the compiler's complexity.
6. **How much does a frontier model need this?** The taxonomy was one *local* model.
   Flux most benefits the local/cheap/private regime; on a frontier model it removes
   a smaller (but nonzero) error class. Worth re-measuring across models.

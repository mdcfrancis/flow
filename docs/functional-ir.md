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

## 3. The computational model: a cell is a pure `reads → writes` function

A cell is a pure function from the record of fields it **reads** to the record of
fields it **writes** (or, for a view, to a **draw list**). It performs no mutation
and no I/O; the *runtime* applies the returned writes to the shared store and renders
the draw list. Purity lives inside the cell; effects live at the boundary.

- **Compute cell** (`run-tick`): `Record(reads) → Record(writes)`. Fields not written
  are unchanged.
- **View cell** (`render-frame`): `Record(reads) → DrawList`. Writes nothing.
- **Effectful cell** (cognition: LLM host calls, `invoke-cell` dispatch): a minority.
  v1 leaves these as raw WAT (see §9 escape hatch); a later effect-primitive model
  can bring them in.

This is *not* full monadic IO — it is a pure function over a record, which is exactly
what these cells already are.

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
each field, the 24-byte draw-record encoding, stack ordering, i32 typing — is the
lowerer's job, and it cannot get any of them wrong.

### 4.2 Grammar (v1, sketch)

```
cell   := (cell NAME (reads ID*) (writes ID*)? body)
body   := (write binding*)          ; compute cell → output record
        | (draw  prim*)             ; view cell → draw list
binding:= (ID expr)
expr   := INT | HEXCOLOR | ID       ; ID is a field read or a let-bound name
        | (let ([ID expr]*) expr)
        | (if expr expr expr)
        | (OP expr*)                ; primitive application
prim   := (rect x y w h color) | (line x1 y1 x2 y2 color) | (circle cx cy r color)
```

### 4.3 Types (v1)

One scalar type: **i32**. The field environment (`reads`) is the typing context;
`let` binds names; `write`/`draw` are the only terminal forms. A single numeric type
makes the `type` failure bucket vanish outright. `f32` (fixed-point vs float, fuel
implications) is a v2 question — see §12.

### 4.4 Primitives (v1)

Arithmetic `+ - * / mod neg`, comparison `< <= > >= = !=`, boolean `and or not`,
`if`, `let`, numeric `min max clamp abs`, output `write`, and draw constructors
`rect line circle` with `#xRRGGBBAA` color literals. Field reads are bare identifiers
resolved against `reads` (an identifier not in `reads` or a `let` is a type error,
caught at lower time and fed back — a *semantic* message, not a stack trace).

Higher-order `map`/`fold` over a small fixed vector (for combinator cells) is v2.

## 5. Lowering to WAT — what the lowerer owns

`Flux AST → typecheck → WAT` is deterministic. The lowerer owns, and therefore
guarantees, every deterministic class we measured:

| Failure class (measured) | Owned by the lowerer |
|---|---|
| `structural` (module, import, export, locals, parens) | emits the wrapper + `run-tick`/`render-frame` export + locals-at-top |
| `stack-sig` (`too many results`, stack order) | allocates locals and orders the stack from the expression tree |
| `type` (i32/i64) | single i32 type; no width to mix |
| `addressing` (`0xB0000+` load/store) | resolves each field name to its offset; emits load on read, store on write |
| draw-record ABI (24-byte layout) | `circle`/`rect`/`line` → the record stream + returns the byte length |
| `empty` (no code) | see §10 — structured emission makes "no code" unrepresentable |

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

## 10. Structured emission kills `empty`

`empty` (model returned prose / no `(module …)`) was 29% of failures — an
instruction-following failure, not a logic one. Flux can be emitted as a **JSON AST
against a schema** via the model's structured-output/tools API, so "no code" is
unrepresentable — the model must return a valid object matching the grammar or the
call is rejected and retried. (S-expression text remains the human-readable form; the
canonical genome serialization — S-expr vs JSON AST — is an open question, §12.)

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

- **Phase 0 — spike (this branch):** define the v1 grammar; hand-write parser +
  typechecker + lowerer for `run-tick`/`render-frame`; unit-test that Flux → WAT
  validates through wazero and behaves. Prove the bouncing-ball physics + renderer
  **converge on gemma via Flux** where raw WAT failed 31×. Decisive and cheap.
- **Phase 1 — synthesis path:** wire Flux into `RunSieve` behind `HDM_IR=1`; model
  emits Flux; raw WAT remains fallback/escape hatch.
- **Phase 2 — genome:** Flux AST as the evolvable genome; mutation/crossover on the
  tree; fusion/memoization as AST rewrites.
- **Phase 3 — combinators & corpus:** `map`/`fold` native; migrate examples/docs.

## 13. Open questions

1. **Numeric type:** stay i32 (fixed-point for sub-pixel motion) or add `f32`? Fuel
   and determinism implications.
2. **Genome serialization:** S-expr text (readable, model-native) vs JSON AST
   (schema-constrained emission kills `empty`). Possibly both — JSON as canonical,
   S-expr as the rendered/readable form.
3. **Effect model:** bring cognition cells in via explicit effect primitives
   (`(reason prompt)`, `(invoke urn args)`) or keep them as WAT indefinitely?
4. **Higher-order scope:** just `map`/`fold` over fixed vectors, or first-class
   lambdas? Bounds the compiler's complexity.
5. **How much does a frontier model need this?** The taxonomy was one *local* model.
   Flux most benefits the local/cheap/private regime; on a frontier model it removes
   a smaller (but nonzero) error class. Worth re-measuring across models.

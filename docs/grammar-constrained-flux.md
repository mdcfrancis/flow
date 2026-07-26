# Grammar-constrained Flux decoding — the north-star first move

Status: plan + confirmed feasibility (exploration). Realizes the north star of
[self-hosting-flux.md §0.1](self-hosting-flux.md): a language optimal for the LLM,
with generation success as the fitness function. This is roadmap #60.

## Why this first

The north star's fitness has four axes: syntax-valid rate, token cost, convergence
speed, canonicality. Grammar-constrained decoding attacks the first **by
construction** — an invalid program becomes undecodable — and it is the lever that
lets us then measure and optimize the other three on a valid baseline. It also
collapses the hardest self-hosting stage (the text parser): if the model can only
emit grammatical Flux, there is no malformed input to recover from.

## Confirmed feasibility (on the live oMLX server)

Probed `http://localhost:8000` (owned_by `omlx`, Qwen3.6-27B-oQ4):

| Field | Enforced? |
|---|---|
| `guided_grammar` (GBNF, vLLM-style) | **YES** — forced `xyzzy` over a greeting; produced a valid Flux cell from a Flux GBNF |
| `json_schema` (`response_format`) | YES — but JSON is not our surface (per the Flux design call) |
| `grammar`, `response_format:{grammar\|regex}`, `guided_regex`, `guided_choice` | no — silently ignored |

So the mechanism is **`guided_grammar` with a GBNF for the Flux S-expr** — no JSON,
surface preserved. First adversarial probe (`grammar: root ::= "ok"` returning
"ok") was a false positive: the model merely complied; the field was ignored.
Always test a constraint with a value the model would never say unprompted.

## The design: a PER-CELL grammar from the layout

The powerful move is that the grammar is generated **per cell, specialized to that
cell's context**, because we know the layout and entry at generation time:

- **Field references are enumerated** — the grammar admits only the cell's declared
  read/write fields (+ the bound capability aliases), so the model literally cannot
  reference an unknown field. This kills the "unknown field" error class too, not
  just syntax.
- **The entry shape is fixed** — a compute cell's grammar ends in `(write (field
  expr) …)`; a view cell's in `(draw <prim> …)`. The model cannot emit the wrong
  entry for its kind.
- **The primitive set is closed** — `+ - * / mod neg abs min max clamp < <= > >= =
  != and or not if`, and the draw prims `circle|rect|line` — enumerated, so no
  invented operators.
- **Literals** — Int/Float/Color/Char token rules mirror the Flux lexer.

What the grammar guarantees by construction: valid syntax, existing fields, the
right entry, known primitives. What it does NOT: type correctness (Int vs Bool vs
Color) and logic — those remain the checker's and the scenarios' job. So this
collapses the *syntax + unknown-field + wrong-entry* failure classes; the model's
remaining work is types and logic, where the feedback loop already works.

## Plan

1. **`flux.GBNF(layout, kind)` → grammar string** — deterministically emit a GBNF
   for the Flux grammar specialized to the cell: enumerated fields + capability
   aliases, the entry shape for the kind, the closed primitive/draw-prim set, and
   the literal token rules. Unit-tested: (a) the emitted grammar parses; (b) a
   corpus of valid cells is accepted and malformed ones rejected by a GBNF checker;
   (c) round-trip — a constrained sample parses under `flux.Parse`.
2. **Wire `guided_grammar` into inference** — add the field to the request body
   (`inference`), and have the Flux synthesis paths (`RunSieveWithLayout`, the
   agentic `flux_check`/authoring) pass `flux.GBNF(...)` when a layout is present.
   Off by default via a flag until measured; a mismatched server just ignores it.
3. **Measure the fitness (the point)** — instrument syntax-valid rate over a grow
   (fraction of first-draft generations that `flux.Parse`+`Check` without repair),
   with and without the grammar. Then token cost and convergence speed on the valid
   baseline. This is the north-star scoreboard.
4. **Optimize the grammar toward LLM-optimal** — once valid-by-construction, evolve
   the grammar for canonicality and terseness (one way to say each thing; drop
   tokens the model wastes), watching token cost + convergence. This is where the
   language begins to drift from the human S-expr toward its speaker.

## Open questions

- **Grammar/checker drift.** The GBNF must stay in lockstep with the participle
  grammar + lexer (`flux/parse.go`). Guard with a test that every corpus cell the
  parser accepts is also accepted by the GBNF, and generate both from one source if
  it drifts.
- **Server portability.** `guided_grammar` is a vLLM/oMLX field; Gemini and other
  providers differ. Keep it optional and provider-gated; the grammar is inert where
  unsupported (the parse+repair loop remains the fallback).
- **Grammar size/latency.** A large enumerated grammar may slow decoding; measure,
  and keep the field set scoped to the cell (already the design).
- **Does it shrink `parse` for self-hosting?** If generation is always grammatical,
  the Flux-hosted compiler needs only check+lower over a structure the model emits —
  confirm once the grammar is the live generation path.

## Status — generator landed and proven live (step 1)

`flux.GBNF(layout, kind)` is implemented and proven end to end on the oMLX server
(guarded round-trip test `TestGBNFRoundTripLive`): under the grammar the model
emits Flux that PARSES and CHECKS with no repair — a compute cell with genuine
integrate + wall-reflect physics (lets + nested exprs) and a view cell drawing the
ball. Two hard-won rules are now baked into the generator:

1. **Bounded everything.** A repetition-prone model exploits any `*` or unbounded
   recursion into non-termination (seen: infinite write-pairs; infinite
   `(clamp (abs …`). All lists are capped (`gbnfMaxList`) and expression nesting is
   stratified to a fixed depth (`gbnfMaxDepth`, `expr0..exprN`); the cell name is a
   fixed constant (it carries no information).
2. **Reads fixed to all fields.** A context-free grammar cannot tie the atoms used
   in the body to a subset declared in a `(reads …)` clause, so the clause is fixed
   to every field — every field reference is then a declared read by construction.
   This exposes the reads/writes clauses as **redundant with the body**: an
   LLM-optimal Flux would *derive* them from the body and drop the clauses (fewer
   tokens, no possible mismatch). A candidate first language-evolution once the
   grammar is the live generation path.

Next: wire `guided_grammar` into the inference request + the Flux synthesis path
(off by default, provider-gated), then measure syntax-valid rate / tokens /
convergence with vs. without — the north-star scoreboard.

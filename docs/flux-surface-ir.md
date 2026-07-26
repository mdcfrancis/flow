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

## Where this goes

- **Now**: the surface is a swappable `Surface` in Go; a language experiment is a new
  `Surface`, gated by `BehaviorHash`. The science loop optimizes the surface with a
  structural invariant.
- **Next — "move the parser into the system"**: make the surface⇄IR mapping a
  data-driven / evolvable artifact rather than fixed Go, so the surface can evolve
  without a code change. The IR stays the contract; `Lower` stays the invariant.
- **The efficiency goal decomposes cleanly onto the surface**: *generated* efficiently
  (grammar the model decodes under — the scoreboard) and *processed* efficiently
  (`Render` compactness/legibility). Both are surface properties; behavior is IR.

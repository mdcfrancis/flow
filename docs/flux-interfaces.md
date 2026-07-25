# Flux interfaces — a cell implicitly implements one or more roles, and the lowerer owns the ABI

Status: design. Companion to [functional-ir.md](functional-ir.md) (the language) and
[flux-as-the-language.md](flux-as-the-language.md) (genome, completeness, stdlib).

## 0. The gap: we ask the model to author an interface we already derive

Today a cell's WebAssembly interface is a **separately authored artifact**, not a
property of its Flux:

- `scaffold` (appgen/grow.go) runs a **per-cell model round-trip**, `g.wit`
  (grow.go:789, `witPrompt` = *"draft a minimal valid WIT contract"*), and stores
  the result at `<urn>:wit`.
- That ref is **written and never read** — nothing downstream consumes it.
- The two entry contracts it can produce, `RunTickContract` and
  `RenderFrameContract` (evolution/sieve.go:47–57), are **the same signature**
  `(i32,i32)->i32`; they differ only by export name (`run-tick` vs `render-frame`).
- And `entryContractFor` (sieve.go:64) **already derives** which one applies from
  the Flux shape: a `(draw …)` terminal ⇒ view, a `(write …)` terminal ⇒ compute.

So in Flux mode we pay an LLM call per cell to write boilerplate we already infer
from the AST, and then ignore it. The interface should live **in Flux** and be
**derived by the lowerer**, not authored by the model.

This doc proposes making that explicit and general: a small set of **interfaces**
(typeclasses) that a cell **implicitly satisfies** from its structure — Go-style,
no `implements` keyword — with the lowerer emitting the ABI for the whole set.

## 1. The model: interfaces as implicitly-satisfied typeclasses

An **interface** is a named role with four parts:

```
Interface {
  Name          // "tick", "view", "input-source", …
  ABI           // exports (name + signature) and required host imports it contributes
  Satisfied(*Cell) bool   // a structural predicate over the CHECKED Flux AST
  Implications  // capability/boundary/scenario obligations that follow (see §3)
}
```

A cell satisfies an interface iff `Satisfied` holds over its typed `flux.Cell`
(reads/writes/body). It may satisfy **one or more**. The lowerer emits the
**union** of the satisfied interfaces' exports and imports; `entryContractFor`
generalizes from "the one entry" to "the set of entries this cell implements."

Nothing is declared. `(cell r (reads ball_x ball_y) (draw (circle …)))` satisfies
`view` because it has a `(draw …)`; it satisfies `input-source` too if it reads an
`hmi_*` field. The Flux *is* the interface declaration.

### Initial registry

| Interface | Satisfied when the cell… | Contributes to the ABI | Implications (§3) |
|---|---|---|---|
| `tick` | has a `(write …)` terminal | exports `run-tick : (i32,i32)->i32` | run-tick regression tapes apply |
| `view` | has a `(draw …)` terminal | exports `render-frame : (i32,i32)->i32` | draw-stream scenarios; no state-write |
| `input-source` | reads an `hmi_*` field | — (reads only) | boundary ⊇ "HMI input"; scenarios seed its `hmi_*` reads; a change signal (§3.3) |
| `stateful` | writes ≥1 contract field | — | participates in shared-state coordination |
| `terminal` *(future)* | body is `[Char] -> [Char]` | exports a stream entry | line-buffered I/O ABI |
| `effectful` *(future)* | imports cognitive/vision | async entry + protected import | never memo-skipped; import-preserving mutation |

`tick` and `view` reproduce today's behavior exactly — the derivation is a
refactor, not a semantics change. The rest is where the model pays off.

## 2. What the lowerer owns

The lowerer already turns a `(cell …)` into a WAT module with one export. Extend it
to:

1. Run `DeriveInterfaces(*Cell) []Interface` against the registry.
2. Emit **each** satisfied interface's export (a cell can have more than one) and
   the union of their required imports.
3. Return the derived interface set as the cell's **interface spec** — a real
   artifact (persisted for the map/inspector), replacing the model-authored WIT.

`EntryContract` becomes one interface's ABI; the sieve's signature check
(`checkEntrySignature`) runs per satisfied interface. `entryContractFor`'s ad-hoc
string sniffing (`strings.Contains(genotype, "(draw")`) is deleted in favor of the
structural predicate over the typed AST — same answer, no string matching.

## 3. Capability implications — the real win (the slider evidence)

Interfaces don't just pick an export; they carry **obligations** the system today
leaves to the model to get right. The slider grow (objective: *"the operator
controls the ball's speed with the first slider"*) is the motivating case. The
model spontaneously wrote the right data flow:

```lisp
(cell input (reads hmi_slider0 hmi_event_seq ball_speed) (writes ball_speed)
  (write (ball_speed (if (> hmi_event_seq 0) hmi_slider0 ball_speed))))
(cell physics … (let ([nx (+ ball_x (* ball_vx ball_speed))] …)))   ; velocity × slider
```

— but it **stalled** for reasons that are exactly `input-source`'s obligations:

### 3.1 Boundary ⊇ "HMI input" (already implicit, make it structural)

A cell only receives live HMI register values if its enforced boundary declares it
reads `"HMI input"` (evolution/appmap.go). The envelope got this right here
(`reads: ["HMI input"]`), but it's the model's job to remember. `input-source`
should **derive** the boundary requirement: reads an `hmi_*` ⇒ boundary must
include "HMI input". No cell that reads a slider can forget to declare it.

### 3.2 Scenarios must seed the cell's `hmi_*` reads

The `input` cell stalled at 2/4 because its acceptance scenarios don't seed
`hmi_slider0`/`hmi_event_seq` — so a correct cell reads 0 and can't be verified.
This is the same gap the keyboard path has. `input-source` carries the obligation
**"my scenarios seed my `hmi_*` reads"**, so the scenario author (appgen/expand.go)
grounds them automatically instead of guessing.

### 3.3 A well-defined change signal

The `input` cell gated on `(> hmi_event_seq 0)` — a reasonable "act on an event"
pattern — but `SetSlider` originally wrote the register like a mouse *move* and
never bumped `hmi_event_seq`, so a drag never fired it. Fixed (commit 8e9bbc4): a
slider move now latches the discrete-event slot (`EvSlider`, index in the key
slot). `input-source` formalizes this: **a source has a change signal on
`hmi_event_seq`**, so gating on it is correct by construction.

Note the shape of these: each is a fact that follows *structurally* from "this cell
reads an HMI input," yet today each is a separate thing the model (or a human) must
independently get right. Folding them into interface satisfaction is the point.

## 4. Integration with the existing pipeline

- **Drop `g.wit` in Flux mode** — the interface spec is derived from the seeded
  no-op Flux (which already fixes the cell's shape) and persisted at `<urn>:wit`
  (or a renamed `<urn>:iface`) for the map. One fewer model round-trip per cell.
- **`entryContractFor` → `DeriveInterfaces`** — one code path, over the typed AST.
- **Boundary evolver** (appgen/boundary.go) consumes `input-source`'s "HMI input"
  implication instead of inferring it from scanned memory access.
- **Scenario authoring** (appgen/expand.go `groundScenarios`) consumes the
  seed-my-inputs obligation — the render-cell rules there are already
  interface-shaped (`view` ⇒ drop state-writes, drop `result`); this generalizes
  that ad-hoc handling into the registry.
- **App map / inspector** show the interfaces a cell implements — a clearer artifact
  than a WIT nobody reads.

## 5. Phasing

- **P1 — derive, don't author.** Interface registry + `DeriveInterfaces`; lowerer
  emits per-interface exports; `entryContractFor` retired; `g.wit` model call
  dropped in Flux mode and replaced by the derived spec. Zero behavior change for
  existing cells (`tick`/`view` reproduce today's choice). Pure de-duplication +
  one fewer model call.
- **P2 — capability implications.** `input-source` drives the boundary declaration
  (§3.1) and scenario seeding (§3.2); the change-signal contract (§3.3) is
  documented against the now-landed `EvSlider`. This is what makes a slider (or any
  HMI-reading) cell converge without the model having to independently rediscover
  each obligation.
- **P3 — new interfaces.** `terminal` (`[Char]->[Char]`, ties to the sequences/Char
  work in functional-ir.md §3), `effectful`/async (cognitive/vision imports),
  multi-entry cells, and `foreign`/`unsafe` (a cell that satisfies an interface via
  a hand-written WAT body — the escape hatch, expressed as "implements the ABI
  directly"). Each is additive: register the interface + its predicate + ABI.

## 6. Risks & open questions

- **Multi-interface cells.** The model *allows* one cell to satisfy both `tick` and
  `view` (write AND draw). HDM's architecture deliberately **splits** these (the
  fracture prompt: "a child that writes state is never render"). Decision: keep the
  *representation* general (a cell may implement many) but let the architecture
  critic keep **preferring** single-responsibility cells. The interface model
  describes capability; it does not mandate fat cells.
- **Explicit escape.** Mostly-implicit is the goal, but an optional
  `(cell x (implements tick) …)` annotation may be needed when a body is ambiguous
  or for `foreign` cells whose WAT the predicate can't inspect. Prefer inference;
  allow annotation.
- **Where the predicate runs.** `Satisfied` must run over the **typed** `flux.Cell`
  (post-check), not raw source — so `input-source` sees resolved `ReadOnly` HMI
  fields, not a substring. This keeps it honest and string-free.
- **Naming.** `<urn>:wit` becomes a derived artifact; renaming to `<urn>:iface`
  clarifies intent but touches the ledger key — cosmetic, do it in P1 or never.

## 6.5 Status (landed)

- **P1 — landed.** `flux/interface.go` (`Interface`, `Registry`, `DeriveInterfaces`,
  `PrimaryEntry`, `EntryOf`); the lowerer emits the entry via `PrimaryEntry`;
  `entryContractFor` derives via `flux.EntryOf`; the per-cell WIT model call is
  dropped in Flux mode and a derived interface spec is persisted instead.
- **input-source grounding — landed, the robust way.** Rather than auto-generate
  fragile directional checks, the fix was to ground the model: subsystems declare
  reads as the generic `"HMI input"` pseudo-field, and the scenario-authoring prompt
  now includes the slider register map (`0x50024..0x50040`) — so the model seeds the
  right offset. **Proven end-to-end:** a "slider controls ball speed" grow converged
  its `input` cell 4/4 with correct `hmi_slider0` seeding, and the `renderer` 7/7.
- **Open — the `stateful` implication (grade-on-writes).** A cell must only be graded
  on fields it WRITES. Observed gap: `input_slider_*` scenarios asserting `ball_speed`
  were mis-assigned to the `physics` cell (which reads but does not write it),
  leaving it unwinnable at 4/6. The fix — drop a coordination reads-postcondition on
  a field the cell does not write — belongs in `groundScenarios`, but that primitive
  is shared across all authoring paths and has suite-erosion hazards, so it needs a
  careful, reviewed change rather than an inline one.

## 7. Recommendation

Do **P1** first: it deletes a redundant per-cell model call and unifies two ad-hoc
`EntryContract`s behind one derivation, with no behavior change — a strict
simplification that also pays for itself in grow latency/cost. Land **P2** next,
because it's what actually makes the sliders (and every HMI-reading cell) converge:
the obligations are already scattered across boundary + scenario code as
special-cases; the registry is where they belong. **P3** follows the language
roadmap (Char/sequences, effects, foreign) and turns each new capability into "add
an interface" rather than "add a special case."

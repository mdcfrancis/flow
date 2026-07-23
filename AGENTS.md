# AGENTS.md — working in this codebase

The working guide for this repo, for humans and coding agents alike. Read it before
changing anything. [README.md](README.md) is the *what & why*; this is the *how it
works, the rules of the road, and how to move it forward*.

> HDM is an experiment (see the README's disclaimer) — optimize for understanding and
> honest change, not for shipping.

## Build, test, run

```sh
go build ./...   # compiles?
go vet ./...     # obvious mistakes?
go test ./...    # suite green? (no network — model calls are faked)
go run .         # boot the runtime + console on 127.0.0.1:8420
```

- The model backend is an LLM reached over localhost HTTP
  (`inference.LocalModelClient`). Put the API key in `.hdm_api_key` (mode 600,
  gitignored); point at a server with `HDM_LLM_URL` / `HDM_LLM_MODEL` /
  `HDM_LLM_PROVIDER`.
- Grow an app at boot: `HDM_APP="a bouncing ball viz…" go run .`
- Useful env: `HDM_DB` (ledger path), `HDM_HEARTBEAT` (evolution tick, e.g. `3s`),
  `HDM_FPS` (frame loop), `HDM_SYS_ONLY`, `HDM_TUI`.
- Watch a running instance over HTTP (`127.0.0.1:8420`) — the fastest way to see what
  it's doing: `/status` · `/cells` · `/cell?urn=…` · `/walk` (goal tree) · `/map` ·
  `/plan` · `/canvas` · `/vision` · `/log` · `/perf`.

## Rules of the road

These break things **silently** — a green build can still violate them.

1. **No new dependencies.** stdlib + `wazero` (Wasm) + `bbolt` (storage), nothing
   else. Need something? Write it — that's the house style (a from-scratch WAT
   assembler, an MVCC store, a fuel meter).
2. **Cells must be deterministic.** No ambient time or randomness — use
   `hdm:kernel/chronos` (`now-ns`, `entropy`). Replay installs a fixed clock and
   seeded entropy; a non-deterministic cell fails replay and can never be optimized.
3. **Never commit the model key.** It lives in the gitignored `.hdm_api_key`. **Stage
   files explicitly — never `git add -A` / `git add .`** so a key can't be swept in.
4. **An optimization must be observably equivalent.** The optimizer may not change a
   cell's exports, imports, signatures, or outputs. New *behavior* is a new cell or a
   growth objective, not an optimization.
5. **The shared-memory map has two sources of truth that must stay in sync:** the
   constants in `execution/hypervisor.go` and the `SHARED MEMORY LAYOUT` block in
   `evolution.Capabilities`. After any change, audit `cells/*.wat` for hard-coded
   absolute offsets — a boundary move that misses one corrupts a neighbor silently.
6. **A scenario must grade a cell through the entry the runtime actually calls** —
   a compute cell via `run-tick`, a renderer via `render-frame`. This is the cell-type
   *ontology* (`appgen/kind.go`): a cell's declared `Kind`, validated against its I/O
   ports, drives genesis, scenario shape, and grading as one fact. Break the tie and
   you get checks a correct cell can never pass.
7. **Green before commit; live-verify the wiring.** `go build && go vet && go test`
   must pass. Anything touching the hypervisor, the memory map, edge services, or a
   cell also needs a boot + `curl` against `127.0.0.1:8420` — unit tests prove logic,
   only a live run proves wiring. Prefer a deterministic probe (`urn:hdm:ui:checkout`)
   over the evolving cells when asserting exact bytes.
8. **Operational.** Kill any running `./hdm` and start from a fresh DB between
   experiments. `hdm.db` is a *live app*, not scratch — copy it (`HDM_DB=copy.db`),
   never mutate the original. Model calls are stochastic and rate-limited; a run that
   doesn't converge may be the backend, not your change.

## The mental model

Three loops run over one substrate: small WebAssembly **cells** that coordinate
through a shared linear memory, addressed by URN (`urn:hdm:apps:<app>:<part>`,
`urn:hdm:sys:*`, `urn:hdm:kernel/*`).

### 1. Runtime — `execution`

`RuntimeManager` (`execution/hypervisor.go`) owns the wazero runtime, the shared
cluster memory, the kernel host modules, cell compilation/caching, the execution
trampoline (read/write masks + software paging), and the edge entry points
(`RoutePacket`, `RenderFrame`, `WriteInputEvent`). Apps tick at frame rate
(`main.go: runFrameLoop`); model-backed cells tick off-thread (`runAsyncLoop`).
Per-cell private memory is software-paged (`execution/pages.go`).

Shared-memory map (constants hold the exact bounds):

```
0x00000  system pointer registry (read-only)
0x10000  inbound packet frame (edge gateway)
0x50000  HMI input register     (mouse/keyboard the host writes, cells poll)
0x51000  canvas draw-output      (render-frame cells write a vector stream here)
0xB0000  sandbox / shared app state (cells keep persistent state here)
0x400000 per-cell private-page window (software paging)
```

### 2. Evolution — `evolution` + `engine` + `telemetry`

The mutation frame (`evolution/orchestrator.go: RunFrame`) drives one cell:
friction discovery → sieve synthesis (the LLM writes a candidate; the hand-written
WAT assembler in `compiler/sieve.go` compiles it) → the gauntlet (replay recorded
tapes in a deterministic shadow, `evolution/executor.go`) → chaos (adversarial
perturbation) → topology (fission/fusion, `evolution/topology*.go`) → MVCC commit
(`engine/mvcc.go`), and only if behavior is preserved and cost drops. Cost is the
Hamiltonian (`telemetry/hamiltonian.go`) over latency, fuel (function-boundary
crossings), tokens, and code size, minus saliency.

### 3. Growth — `appgen`

`Grower` (`appgen/grow.go`) turns a natural-language objective into an app:
objective → **envelope** (subsystems, their I/O ports, and declared `Kind`) →
**contract** (the shared-state field layout) → **plan** (per-component design) →
**genesis** (a crude first cell per subsystem) → **acceptance scenarios**
(`evolution/scenario.go` — seed memory, run the entry N times, assert on
reads/draw/trajectory). A cell that stalls is **fractured** into simpler sub-cells
(`appgen/expand.go`); a subsystem judged too hard up front is decomposed proactively
(`appgen/difficulty.go`). Cells co-evolve — they build up alongside each other under
the heartbeat, not one at a time.

The heartbeat driver is the fixpoint in `main.go`: best-effort passes (`retryStalled`,
`fractureStalled`, `refreshAppMaps`, `challengeArchitectures`, `visualCritic`,
`codeCritic`, …) run each tick, each individually fail-safe.

## Where to start reading

| If you want to… | Start at |
|---|---|
| understand boot + the heartbeat | `main.go` (`main`, then the fixpoint passes) |
| change how a cell runs / the memory map | `execution/hypervisor.go` |
| change the evolution loop or add a gate | `evolution/orchestrator.go`, `evolution/executor.go` |
| change how apps are grown | `appgen/grow.go`, `appgen/plan.go`, `appgen/expand.go` |
| change cell typing / acceptance shape | `appgen/kind.go`, `evolution/scenario.go` |
| add or inspect an HTTP endpoint | `integration/gateway_ui.go` + the `Services{…}` literal in `main.go` |
| read a hand-written reference cell | `cells/*.wat` (`life`, `router`, `ui`, …) |

Package-by-package responsibilities are in [CONTRIBUTING.md](CONTRIBUTING.md#repo-layout).

## How to move it forward

**Default to growing behavior, not hand-coding it.** If the feature is application
logic, add/adjust an `HDM_APP` objective or a genesis skeleton in `appgen` and let the
loop build it. Reserve hand-written cells (`cells/*.wat`) for runtime primitives and
reference implementations.

Extension seams, and where they plug in:

- **A new cell kind** — the ontology is built to extend: add one entry to `cellKinds`
  in `appgen/kind.go`. It binds the entry export, genesis contract, scenario shape,
  and port invariant in one place — no scattered edits.
- **A new kernel host service** — register the module in `execution/hypervisor.go`,
  add a deterministic stub in `evolution/executor.go` (so replay still instantiates
  candidates), and describe it in `evolution.Capabilities` so synthesized cells know
  it exists.
- **A new evolutionary gate** — a *pure veto* between sieve and commit: given baseline
  + candidate it returns pass/fail and never mutates live state (see `SurvivesChaos`).
  Wire it into the frame; a failure is logged and skipped, never fatal.
- **A new edge endpoint** — a handler in `integration/gateway_ui.go`, added to
  `Services` and wired in `main.go`; it translates an external protocol into shared
  memory + a `RuntimeManager` call, never reaching into cell internals.
- **The prompts and policy are themselves evolvable** — steering prompts, the
  Hamiltonian coefficients, and thresholds live in the ledger and can be tuned. Treat
  the outer Go as security + rollback and push behavior into that evolvable surface.

Then follow the workflow in [CONTRIBUTING.md](CONTRIBUTING.md): work on `main`, stage
explicitly, keep the suite green, and live-verify.

## Traps

- **The WAT assembler** wants all `(local …)` at the top of a function — mid-function
  locals are rejected. Import shared memory as `(memory 100)`.
- **Grade-through-the-wrong-entry** is the classic silent failure — see rule 6.
- **Non-determinism fails replay, not the build** — a cell that reads wall time or
  randomness looks fine until the gauntlet can't reproduce it (rule 2).
- **Manual mutation vs the heartbeat** — the `/structural` and `/build` paths pause
  the evolution loop for the frame to avoid MVCC conflicts; a raw concurrent ledger
  write will lose to the heartbeat.
- **`hdm.db` is a real app** — don't run experiments against it; copy it first.

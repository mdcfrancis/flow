# Contributing

A few conventions here are easy to break and expensive to undo. This is the short
version.

## Hard rules

1. **No new dependencies.** The module is the Go standard library plus exactly
   `github.com/tetratelabs/wazero` (Wasm) and `go.etcd.io/bbolt` (storage)
   (`golang.org/x/sys` is a transitive indirect). Don't add a third-party package —
   if you need something, write it. The whole thing is from-scratch on purpose: a
   hand-written WAT assembler, an MVCC store, a fuel meter. That's the house style.
2. **Cells are deterministic.** A cell must not read ambient time or randomness —
   use `hdm:kernel/chronos` (`now-ns`, `entropy`). Replay installs a fixed clock and
   seeded entropy; a non-deterministic cell fails replay and can never be optimized.
3. **Never commit the model API key.** It lives in the gitignored `.hdm_api_key`
   (mode 600). Stage files explicitly — never `git add -A` / `git add .` — so the key
   can't be swept into a commit.
4. **A rewrite must be observably equivalent.** The optimizer must not change a
   cell's exports, imports, signatures, or outputs. New *behavior* is a new cell or a
   growth objective, not an optimization.
5. **Everything builds, vets, and tests before commit.**
   `go build ./... && go vet ./... && go test ./...` must be green.

## Workflow

```sh
go build ./...   # compiles?
go vet ./...     # no obvious mistakes?
go test ./...    # suite green?
go run .         # boot and exercise the change against the live console
```

Work on `main`; don't push to a remote unless asked; stage the specific files you
changed (see rule 3).

Anything touching the hypervisor, the memory map, edge services, or a cell needs
**live verification** — unit tests prove logic, but a boot + `curl` against
`127.0.0.1:8420` proves the wiring. Prefer a deterministic probe (e.g. the static
`urn:hdm:ui:checkout` cell) over the evolving life cell when asserting exact bytes.

## Repo layout

| Package | Responsibility |
|---------|----------------|
| `storage` | `LedgerEngine` over bbolt: content-addressed blocks + named refs. |
| `manifest` | `NodeDescriptor` (genotype/phenotype linkage), `Repository`, semantic + dependency metadata. |
| `compiler` | `CompilerService`: hand-written WAT→Wasm assembler, verified by wazero; `MemoryGuard` bounds-checks stores. |
| `execution` | `RuntimeManager` — the hypervisor: wazero runtime, shared cluster memory, kernel host modules, cell resolution, the trampoline, and the edge entry points. |
| `inference` | `LocalModelClient`: OpenAI-style chat client over localhost HTTP to the local model server. |
| `telemetry` | `FuelTracker`, per-call fuel listener, and the Hamiltonian cost function. |
| `evolution` | The orchestrator and every stage of the loop: friction, sieve synthesis, gauntlet (tape replay), chaos, topology (fission/fusion), tapes + janitor, acceptance suites, cell registry. |
| `engine` | `MVCCCoordinator`: optimistic-concurrency commit of a new phenotype against the manifest root. |
| `codependency` | Co-mutation tracking: which cells change together, feeding fusion. |
| `tapes` | Wire format for recorded replay tapes. |
| `gc` | Mark-and-sweep over the block store, rooted at the manifest. |
| `appgen` | `Grower`: natural-language objective → app envelope → subsystem genesis → enrolled cells. |
| `axiom` | `Compiler`: natural-language *target* → `Target` applied to live cells. |
| `integration` | Edge HTTP services: ingress gateway, canvas viewer, build console, input gateway, status feed. |
| `status` | `Broker`: a thread-safe activity feed the console polls at `/status`. |
| `tui` | Optional terminal dashboard. |

## Code style

- Match the surrounding code: naming, comment density, idiom. Most exported symbols
  carry a short doc comment explaining *why*, not just *what*.
- Keep host-boundary code (anything touching shared memory) explicit about offsets
  and lengths; annotate magic addresses with their symbolic name.

## A few recipes

**Add a hand-written cell (`cells/*.wat`).** All `(local …)` go at the top of a
function (the assembler rejects mid-function locals). Import the shared memory
(`(import "hdm:kernel/hardware-io" "shared-cluster-memory" (memory 100))`), export
exactly one entry contract (`run-tick` / `route-packet` / `render-frame`), keep
persistent state in the sandbox (`0xB0000+`), and verify it compiles through the
sieve (`compiler.NewCompilerService().CompileGenotype`) before wiring it into
`main.go`. See `execution/life_test.go` for the test pattern.

**Add a kernel host service.** Register the module in `execution/hypervisor.go`
(`NewHostModuleBuilder("hdm:kernel/your-service")`), add a deterministic stub in
`evolution/executor.go` so replay still instantiates candidate cells, and describe
the export in `evolution.Capabilities` so synthesized cells know it exists.

**Change the shared-memory map** — the highest-risk change. The layout is asserted
in two places that must stay in sync: the constants in `execution/hypervisor.go` and
the `SHARED MEMORY LAYOUT` block in `evolution.Capabilities`. Then audit `cells/` for
hard-coded absolute offsets — a boundary move that misses one corrupts a neighbor
silently.

**Add an evolutionary gate.** New checks live in the `evolution` mutation-frame path
between sieve and commit. A gate is a pure veto: given baseline + candidate it returns
pass/fail (like `SurvivesChaos`) and never mutates live state. A failure must be
logged and skipped, never fatal.

**Prefer growing behavior to hand-coding it.** If the feature is application logic
rather than runtime plumbing, add/adjust an `HDM_APP` objective or a genesis skeleton
in `appgen` instead of hand-writing a cell. Reserve hand-written cells for runtime
primitives and reference implementations.

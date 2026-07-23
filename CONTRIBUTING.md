# Contributing

Start with **[AGENTS.md](AGENTS.md)** — it has the rules of the road (the invariants
that break silently), the mental model, and how to extend the system. This file covers
the mechanics and the package map.

## Workflow

```sh
go build ./...   # compiles?
go vet ./...     # no obvious mistakes?
go test ./...    # suite green?
go run .         # boot and exercise the change against the live console
```

- Work on `main`; don't push to a remote unless asked.
- **Stage the specific files you changed — never `git add -A` / `git add .`.** The
  model key lives in the gitignored `.hdm_api_key` and must never be swept into a
  commit.
- Anything touching the hypervisor, the memory map, edge services, or a cell needs
  **live verification** — a boot + `curl` against `127.0.0.1:8420` proves wiring that
  unit tests can't. Prefer a deterministic probe (`urn:hdm:ui:checkout`) over the
  evolving cells when asserting exact bytes.

## Repo layout

| Package | Responsibility |
|---------|----------------|
| `storage` | `LedgerEngine` over bbolt: content-addressed blocks + named refs. |
| `manifest` | `NodeDescriptor` (genotype/phenotype linkage), `Repository`, semantic + dependency metadata. |
| `compiler` | `CompilerService`: hand-written WAT→Wasm assembler, verified by wazero; `MemoryGuard` bounds-checks stores. |
| `execution` | `RuntimeManager` — the hypervisor: wazero runtime, shared cluster memory, kernel host modules, cell resolution, the trampoline, and the edge entry points. |
| `inference` | `LocalModelClient`: OpenAI-style chat client over localhost HTTP to the model server. |
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
- Keep host-boundary code (anything touching shared memory) explicit about offsets and
  lengths; annotate magic addresses with their symbolic name.

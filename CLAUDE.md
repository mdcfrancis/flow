# CLAUDE.md

This project keeps its working guide in **[AGENTS.md](AGENTS.md)** — the same file
humans and agents use. Read it before changing anything: it covers the build/test/run
loop, the rules of the road (the invariants that break silently), the mental model
with file pointers, and how to extend the system.

Quick reminders that matter most here:

- Build/test/run: `go build ./... && go vet ./... && go test ./...`, then `go run .`
  (console on `127.0.0.1:8420`).
- **Stage files explicitly — never `git add -A`.** The model key lives in the
  gitignored `.hdm_api_key`.
- Live-verify anything touching the hypervisor, memory map, edge services, or a cell —
  a boot + `curl` against `127.0.0.1:8420` proves wiring that unit tests can't.
- `hdm.db` is a live app, not scratch — copy it (`HDM_DB=copy.db`), never mutate it.

# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project

Go CLI that extracts the users and projects of one Asana workspace into one JSON file per resource (`output/users/<gid>.json`, `output/projects/<gid>.json`), every 5m (default), every 30s, or once. **Stdlib only** — no third-party modules without explicit approval. Auth: Personal Access Token from `ASANA_ACCESS_TOKEN` (env or `.env`, never a flag). The directory is not a git repo (`git.enabled: false` in `.ai-factory/config.yaml`).

## Commands

```sh
make check                                   # gofmt check + vet + test + race + build
go test ./...                                # all tests (offline, mock Asana server)
go test -race ./...
go test ./internal/scheduler -run TestOverrun -v   # single test
go vet ./...
gofmt -l .                                   # no output = formatted
go run ./cmd/asana-extractor --once          # needs .env (copy .env.example)
make live-test                               # optional, real API; skipped unless ASANA_LIVE_TEST=1
```

## Architecture

Layered, concrete types, no cross-layer interfaces (see `.ai-factory/ARCHITECTURE.md`):

`cmd/asana-extractor` (config → wiring → once | scheduler, exit codes 0/1/2) → `internal/extractor` (users then projects, page by page) → `internal/asana` (HTTP client, pacer, retries) + `internal/storage` (atomic temp+rename writes). `internal/scheduler` does immediate + fixed-grid runs with skip-on-overrun; `internal/config` resolves flag > env > `.env` > default; `internal/asanatest` is the fake Asana API used by tests.

Invariants that span packages — keep them when changing code:

- Every HTTP attempt, including retries, goes through the single `pacer`; a 429 `Retry-After` becomes a shared cooldown and must never be capped or shortened.
- Pagination stops only on `next_page == null`; filters/`opt_fields`/`limit=100` are resent on every page; only enum-documented `opt_fields` are requested (`owner`, not `owner.name`).
- List callbacks write each page (one atomic temp+rename file per resource) before the next fetch (bounded memory); files of resources absent from later runs are kept.
- Anything that waits takes injectable `now`/`sleep` so tests stay deterministic — no real sleeps for scheduling in tests.
- Never log the token, headers, or resource payloads; errors carry op + status only.

## Context files

- `README.md` — setup, config table, retry policy, scheduling semantics, scale analysis, limitations
- `PLAN.md` — implementation checklist with acceptance criteria
- `.ai-factory/research/asana-extractor-exercise/RESEARCH.md` — verified Asana API facts and design decisions
- `.claude/skills/asana-api/` — Asana API reference skill

# Architecture: Layered

## Overview

`asana-extractor` is a single-purpose CLI: resolve config → list users and projects of one Asana workspace → write one JSON array file per resource type, once or on a schedule. Beyond pagination, pacing, retries and atomic writes there is no business logic, so a layered structure is enough: CLI wiring on top, one orchestration package, two leaf packages for I/O.

Concrete types are wired directly (no interfaces between layers): each seam is tested through real boundaries instead of mocks — `httptest` fake Asana (`internal/asanatest`), temp directories, and injected `now`/`sleep` functions for time.

## Decision Rationale

- **Project type:** CLI data extractor (Asana REST API → JSON files)
- **Tech stack:** Go (go.mod `go 1.24`), stdlib only
- **Key factor:** two resource types, one source, one sink — low domain complexity; take-home scope rewards clarity over abstraction

## Folder Structure

```
cmd/asana-extractor/     main.go: signals, config.Load, logger, wiring, once vs scheduler, exit codes (0/1/2)
internal/config/         flags + env + .env parsing, precedence, validation
internal/asana/          Client: bearer auth, paginated ListUsers/ListProjects (page callbacks), pacer, retries, APIError
internal/storage/        Store.Begin(kind) → ArrayWriter.Add/Commit/Abort: streamed array, atomic temp+rename per type, CleanupTemp
internal/extractor/      Extractor.Run: users then projects, page by page → storage; stats + result logging
internal/scheduler/      immediate run + fixed-grid schedule, skip overruns, no backlog
internal/asanatest/      in-memory fake Asana API (test-only helper)
```

## Dependency Rules

```
cmd/asana-extractor → config, asana, storage, extractor, scheduler
extractor           → asana, storage
asana, storage, scheduler, config → stdlib only
asanatest           → stdlib only (imported by tests)
```

- ✅ `main` constructs `asana.Client` and `storage.Store` and passes them to `extractor.New`
- ✅ `extractor` uses `asana.User` / `asana.Project` as its data model and writes them unchanged
- ❌ `asana`, `storage`, `scheduler` importing each other or `extractor` / `config`
- ❌ Any package besides `cmd` calling `os.Exit`, parsing flags or reading the process environment (`config.Load` receives `getenv`/`readFile`)
- ❌ Interfaces added only "for mocking" — test through `asanatest`, temp dirs and injected clocks

## Layer Communication

- `context.Context` first on every call; `main` derives it from `signal.NotifyContext(SIGINT, SIGTERM)`.
- Listing is callback-per-page (`func([]T) error`) so the extractor writes a page before the next is fetched — memory bounded by one page; a callback error stops paging.
- Errors are wrapped with operation context (`list users page 2: …`); `asana.APIError` exposes the status; the extractor joins users/projects errors; `main` maps the outcome to an exit code.
- `*slog.Logger` is injected; no globals. Never log the token, headers or resource payloads.

## Key Principles

1. **Thin main:** `run(ctx, args, getenv, readFile, stderr) int` is the testable entry point.
2. **One pacer per process:** every request, including retries, passes through it; 429 `Retry-After` becomes a shared cooldown and is never shortened.
3. **Atomic, idempotent output:** temp file in the destination dir → fsync → rename; one streamed array per resource type, renamed into place only after a complete listing; unchanged content is not rewritten.
4. **Deterministic time:** anything that waits takes injectable `now`/`sleep`.
5. **Fail per run, not per process:** periodic mode logs failed runs and continues; `--once` exits nonzero.

## Code Organization Note

- New resource types (teams, tasks): add a `List…` method + type in `asana`, a `Kind…` in `storage`, a step in `extractor.Run`.
- Evolution trigger: more than ~5 resource types, or a second sink (DB/object store) → consider per-resource packages and a sink interface at that point, not before.

## Anti-Patterns

- ❌ Building URLs, setting headers or parsing `Retry-After` outside `asana`
- ❌ Writing files outside `storage` or writing in place without temp+rename
- ❌ Collecting all pages into a slice before writing
- ❌ `http.DefaultClient` (no timeout) or `time.Sleep` without context
- ❌ A `utils`/`common` package

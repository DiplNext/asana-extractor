# Implementation Plan — Asana Extractor

Design rationale and API evidence: `.ai-factory/research/asana-extractor-exercise/RESEARCH.md`.

## Steps

- [x] **1. Scaffold** — `go.mod`, `.gitignore`, `.env.example`, `Makefile`.
  - AC: `go build ./...` works on an empty skeleton; `.env`, `output/`, `bin/` ignored.
- [x] **2. Config** (`internal/config`) — flags, env, `.env` loader, defaults, validation.
  - AC: precedence flag > process env > env file > default; invalid values fail with a message naming the key; token never read from flags.
- [x] **3. Asana client** (`internal/asana`) — `net/http` client, bearer auth, paginated `ListUsers` / `ListProjects` with page callbacks, typed `APIError`.
  - AC: `limit=100`, filters and `opt_fields` kept on every page, stops only on `next_page == null`, GIDs are strings, errors carry op + status without secrets.
- [x] **4. Pacing + retries** (`internal/asana`) — shared pacer with 429 cooldown; retry policy.
  - AC: every attempt (incl. retries) goes through the pacer; `Retry-After` honored exactly; missing/invalid → jittered backoff; 4xx not retried; retries bounded; all waits cancel with ctx.
- [x] **5. Storage** (`internal/storage`) — atomic JSON array writer (one file per resource type; changed from one file per resource on user request, see below).
  - AC: `users.json`, `projects.json`, one JSON array each; streamed page by page into a temp file in the dest dir, renamed only after the full listing; temp removed on failure; existing file intact on failure.
- [x] **6. Extractor** (`internal/extractor`) — one run: users then projects, page by page, counts + retries + duration.
  - AC: memory bounded to one page; a failure in one resource type still attempts the other; result reports counts and error.
- [x] **7. Scheduler** (`internal/scheduler`) — immediate run, fixed grid, skip overruns, no backlog.
  - AC: deterministic tests with fake clock for 5m and 30s, overlap skip, no catch-up, cancellation.
- [x] **8. CLI wiring** (`cmd/asana-extractor`) — signals, logging, exit codes, offline E2E test.
  - AC: `--once` exits 1 on failure, 0 on success; config errors exit 2; periodic mode survives failed runs; SIGINT/SIGTERM stop cleanly.
- [x] **9. Docs** — `README.md` (setup, config, intervals, output, retries, scheduling, scale math, limitations, test commands).
- [x] **10. Verify** — `gofmt -l .`, `go vet ./...`, `go test ./...`, `go test -race ./...`, `go build ./...`; review against requirements; fix findings.

## Acceptance (overall)

All commands in step 10 pass with evidence in the final report; no credentials in any file; live test is opt-in only.

## Verification log (2026-09-24)

- `gofmt -l .` → no output; `go vet ./...` → clean; `go build ./...` → ok.
- `go test ./...` and `go test -race ./...` → all packages ok; `go test -race -count=5 ./...` → no flakes.
- Real binary: SIGTERM during retry backoff → `extraction interrupted status=canceled`, `shutdown complete`, exit 0, token absent from logs.
- Independent review found 1 medium defect (429 `Retry-After` dropped when retries were exhausted) → fixed + regression test (verified to fail on the old code). Low findings fixed: Retry-After overflow, rate-limit upper bound (1500), redirects not followed, token character validation, `.env` inline comments, temp-file cleanup age check, vacuous/flaky tests, README gaps.
- `opt_fields` checked against the documented enum: `owner.name` is not listed for `GET /projects`, so only `owner` is requested.
- Second self-review: config parse errors now reported together with validation errors (was short-circuiting); a second SIGINT/SIGTERM now force-exits. Re-verified with the real binary.
- Live run by the user (2026-09-24 15:11, `make run-once`): `status=success`, users=2, projects=2, written=4, retries=0, 1.37s. Output files are single objects with the expected fields; `owner` is returned as `{gid}` only (name available via the user's entry). Only 2 of the required ≥3 users are visible — third invitation presumably not yet accepted; re-run after acceptance.
- Second live run (15:13): `written=0 unchanged=4` — idempotent upserts confirmed on real data; still users=2.

## Change: JSON array output (2026-09-24 15:21, user request via `/aif-fix`)

- Output changed from `output/<kind>/<gid>.json` (one object per file) to `output/users.json` and `output/projects.json` (one JSON array each).
- Regression check: `TestOnceEndToEnd` + `TestEmptyWorkspaceWritesEmptyArrays` rewritten to expect the arrays; failed before the change (`output dir contents: [projects users]`), pass after.
- Storage streams elements into a temp file and renames only after the full listing succeeds; a failed listing keeps the previous file (`TestFailedListingKeepsPreviousFileAndStillExtractsProjects`).
- Live run after the change (2026-09-24 15:23, `make run-once`): `status=success`, users=3, projects=2, `users.json` and `projects.json` written as arrays in 1.31s, retries=0. Exercise minimums (≥3 users, ≥2 projects) met.

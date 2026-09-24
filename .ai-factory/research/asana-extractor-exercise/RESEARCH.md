# Research: Asana Extractor exercise

Updated: 2026-09-24 15:29
Status: active
Index: [INDEX.md](INDEX.md)

## Active Summary (input for /aif-plan)
<!-- aif:active-summary:start -->
Topic: Go CLI that extracts Asana users and projects of one workspace into one JSON file per resource, on a 5m (default) or 30s schedule, or once.
Goal: Reviewable take-home solution: stdlib-only Go, clear package boundaries, deterministic offline tests, README with setup, retry policy, scheduling and scale analysis.
Scope: In — PAT auth, `GET /users?workspace=`, `GET /projects?workspace=`, pagination, pacing, retries, atomic per-file storage, scheduler, graceful shutdown, docs. Out — deletion reconciliation, webhooks/events, multi-instance coordination, OAuth, databases.
Constraints:
- Stdlib only (no CLI framework, no Asana SDK, no x/time/rate). Token only from env/.env, never flags/logs/files.
- Asana facts (verified, see Sessions → Evidence): `limit` 1–100 with opaque `next_page.offset`, `next_page: null` ends the listing, offsets expire; 429 carries `Retry-After` in seconds; rejected requests still count against quota; standard quota 150 req/min free, 1,500 paid; concurrent limit 50 GET; separate cost-based limit also returns 429.
- `GET /workspaces/{gid}/users` caps at 2,000 users → use `GET /users?workspace=` (sorted by user ID, paginated).
- `GET /projects` "may timeout for large domains"; `team` filter is deprecated → still use `workspace` filter with `limit=100`, document risk.
- `GET /users` / `GET /projects` expose no `modified_since` filter → every run is a full listing.
Requirements:
- Config keys: `ASANA_ACCESS_TOKEN`, `ASANA_WORKSPACE_GID`, output dir, interval, HTTP timeout, rate limit, max retries; flags `--interval`, `--once`; precedence flag > process env > `.env` file > default; validate at startup (exit 2).
- Output `output/users/<gid>.json`, `output/projects/<gid>.json`, one object per file, atomic temp+rename in the same dir, temp cleanup, no deletion of absent resources. (The JSON-array layout tried on 2026-09-24 was reverted on user request; see Sessions.)
- Scheduler: immediate first run, one run at a time, skipped slots during overrun, no catch-up, next start is the first grid slot strictly in the future.
- Periodic mode logs failures and continues; `--once` exits nonzero on failure.
Decisions:
- Sequential requests + one shared pacer (min spacing = 60s / rate) that also holds a shared 429 cooldown; default 60 req/min (40% of free-tier quota, leaves headroom for other apps on the same token).
- Retries: transport errors, 429, 500/502/503/504; max retries default 5; 429 with valid `Retry-After` waits exactly that (never capped); missing/invalid → equal-jitter exponential backoff (base 1s, cap 30s). 4xx (400/401/402/403/404) never retried. 200 with malformed JSON is permanent.
- Archived projects included (no `archived` param); `archived` field written to each file.
- Schemas: user `{gid,name,email}`; project `{gid,name,archived,completed,color,created_at,modified_at,due_on,start_on,privacy_setting,permalink_url,owner,team,workspace}` with nullable refs; values requested through `opt_fields`, no per-resource detail calls.
- GIDs are strings and must match `^[A-Za-z0-9_-]{1,64}$` before becoming filenames.
- Packages: `cmd/asana-extractor`, `internal/config`, `internal/asana`, `internal/storage`, `internal/extractor`, `internal/scheduler`; time injected via `now`/`sleep` funcs.
Risks:
- Pending invitations: API visibility of invited-but-not-accepted users is not documented; README instructs verifying via `GET /users?workspace=` instead of assuming.
- Large domains: `/projects` timeouts; mitigated by pagination + retry, documented as limitation.
- 30s interval can't guarantee fresh complete data when requests/run × pacing > 30s.
Open questions:
- Exact behavior of pending invitees in `/users` (manual verification only; no live credentials in this session).
Success signals: `gofmt -l .` empty; `go vet`, `go test ./...`, `go test -race ./...`, `go build ./...` pass; E2E offline test writes expected files; README scale math present.
Next step: Write `PLAN.md` and implement.
<!-- aif:active-summary:end -->

## Sessions
<!-- aif:sessions:start -->
### 2026-09-24 14:27 — API verification and design
What changed: Verified Asana API behavior and fixed design decisions for implementation.
Key notes:
- Evidence (Context7 `/websites/developers_asana`, `/websites/developers_asana_reference`): pagination semantics and offset expiry; `Retry-After` 429 example; cost limits; concurrent limits by method; `/workspaces/{gid}/users` limited to 2,000; `/users` "sorted by user ID".
- Evidence (official docs fetched directly because Context7 lacked the numbers — fallback): https://developers.asana.com/docs/rate-limits (150/1,500 per minute, 50 concurrent GET, rejected requests count against quota, `Retry-After` in seconds); https://developers.asana.com/reference/getusers (params `workspace`, `team`, `limit`, `offset`; opt_fields `email`, `name`, `photo`, `workspaces`); https://developers.asana.com/reference/getprojects ("may timeout for large domains", `team` filter deprecated, `archived` optional with no default); https://developers.asana.com/docs/workspace-events (Enterprise+ service account only, sync token expires after 4h inactivity, excludes "Workspace - changed"); webhooks guide (workspace-level filters: Project added/removed/deleted/undeleted/changed, Workspace Membership added/removed — no user profile change events); https://developers.asana.com/docs/personal-access-token (PAT from https://app.asana.com/0/my-apps).
- Inference: incremental extraction for users is not supported by any listing filter; projects could be webhook-driven but still need periodic full reconciliation.
- Coherence gate: pass (summary claims each map to an evidence line above; the pending-invite behavior is marked unknown).
Links (paths): `.ai-factory/DESCRIPTION.md`, `.ai-factory/ARCHITECTURE.md`, `.claude/skills/asana-api/SKILL.md`
### 2026-09-24 14:55 — Implementation review corrections
What changed: Two design facts were corrected during implementation and review.
Key notes:
- Evidence (https://developers.asana.com/reference/getprojects, fetched directly): the documented `opt_fields` enum for `GET /projects` includes `owner`, `team.name`, `workspace.name` but not `owner.name`; the implementation requests `owner` only. Supersedes nothing in the Active Summary (it did not list `owner.name`).
- Decision refinement: a 429 cooldown must be applied even when retries are exhausted, so the next page/resource/run respects `Retry-After`; rate limit bounded to 1–1500 req/min (documented paid quota).
- Coherence gate: pass.
Links (paths): `internal/asana/client.go`, `internal/asana/types.go`, `PLAN.md`
### 2026-09-24 15:21 — Output format changed to JSON arrays
What changed: The user requested all users in one file and all projects in one file as JSON arrays, replacing the exercise's original one-object-per-file layout.
Key notes:
- Superseded: "one object per file" / `<kind>/<gid>.json` layout and GID-as-filename validation.
- New behavior: `output/users.json`, `output/projects.json`; streamed (memory bounded by one page), atomic rename only after a full listing, identical content not rewritten; deleted/inaccessible resources drop out on the next successful run (no stale entries), but the two files are published independently.
- Coherence gate: pass (Active Summary Requirements/Decisions updated to match).
Links (paths): `internal/storage/storage.go`, `internal/extractor/extractor.go`, `README.md`
### 2026-09-24 15:29 — Output format reverted to one file per resource
What changed: At the user's request the JSON-array output (`users.json`, `projects.json`) was reverted to the exercise's original layout `output/<kind>/<gid>.json`, one object per file.
Key notes:
- Superseded: the "Output format changed to JSON arrays" session above.
- Restored behavior: per-resource atomic temp+rename writes, GID filename validation, identical content not rewritten, files of absent resources kept, per-file consistency (a failed run keeps pages already written).
- Coherence gate: pass (Active Summary Requirements/Decisions restored).
Links (paths): `internal/storage/storage.go`, `internal/extractor/extractor.go`, `README.md`
<!-- aif:sessions:end -->

# asana-extractor

Go CLI that authenticates to Asana with a personal access token (PAT), extracts the **users** and **projects** of one workspace, and writes each resource type to one JSON array file (`output/users.json`, `output/projects.json`). It runs every 5 minutes (default), every 30 seconds, or once.

- Go standard library only (no third-party modules).
- Sequential, paced requests; bounded retries that honor `Retry-After`.
- Atomic writes; a file is only ever replaced by a complete listing, never left truncated.

```sh
cp .env.example .env         # fill in token + workspace GID
make run                     # every 5 minutes (default)
make run-30s                 # every 30 seconds
make run-once                # single run, exit status reflects success
```

## 1. Asana account setup (manual prerequisites)

These steps need a browser and email inboxes you control. Nothing here is automated, and nothing in the repository contains credentials.

1. **Account and workspace.** Sign up at <https://asana.com> with an email you control; the free plan is enough. Signing up creates a workspace or organization. To add another one, use the profile menu → *Create new workspace* (the exact label may differ between Asana UI versions).
2. **At least three test users.** Invite three addresses you control, e.g. provider aliases like `you+asana1@example.com` if your mail provider supports plus-addressing, or separate test mailboxes. **Open each invitation and accept it** (set a name and password). A pending invitation is not the same as an accepted membership: don't assume invited-but-not-accepted people show up, or show up with the same fields, in the API. Check with step 5.
3. **At least two projects.** Create two or more projects in that workspace. Optionally archive one to see that archived projects are exported with `"archived": true`.
4. **Personal access token (no OAuth).** Open the developer console at <https://app.asana.com/0/my-apps>, create a *personal access token*, give it a description, and copy it once. Put it only in your local `.env` (git-ignored) or your shell environment. Treat it like a password: never commit it or paste it into chats, tickets or logs. Revoke it in the same console when you're done.
5. **Workspace GID and visibility check.** Load `.env` into your shell so the token doesn't land in shell history, then query the API:

   ```sh
   set -a; . ./.env; set +a

   # List workspaces visible to the token: copy the "gid" of yours into ASANA_WORKSPACE_GID in .env
   curl -s -H "Authorization: Bearer $ASANA_ACCESS_TOKEN" \
     https://app.asana.com/api/1.0/workspaces

   # Confirm the test users are visible (expect >= 3 entries, accepted members)
   curl -s -H "Authorization: Bearer $ASANA_ACCESS_TOKEN" \
     "https://app.asana.com/api/1.0/users?workspace=$ASANA_WORKSPACE_GID&opt_fields=name,email&limit=100"
   ```

   Or run the opt-in live test, which prints only counts: `make live-test`.

The PAT carries the permissions of the user who created it. The extractor exports only users and projects **visible to that user**; it makes no claim to organization-wide completeness (private projects the user can't see are not exported).

## 2. Configuration

Configuration is resolved once at startup and validated before any API call. Invalid configuration prints every problem and exits with status `2`.

**Precedence (highest first):** command-line flag → process environment → env file → built-in default.

The env file is `.env` in the current directory by default (a missing default file is ignored) or the path given with `--env-file` (which then must exist). It's a plain `KEY=VALUE` file: `#` comments, optional `export ` prefix, optional matching quotes, no variable expansion. Values from the file never override variables already set in the process environment; a variable set to the empty string counts as unset. For unquoted values, ` #` starts an inline comment.

| Setting | Env var | Flag | Default | Validation |
|---|---|---|---|---|
| Access token | `ASANA_ACCESS_TOKEN` | — (env/file only) | — | required, no whitespace/control chars |
| Workspace GID | `ASANA_WORKSPACE_GID` | `--workspace` | — | required, numeric |
| Output directory | `ASANA_OUTPUT_DIR` | `--output-dir` | `output` | non-empty |
| Extraction interval | `ASANA_EXTRACT_INTERVAL` | `--interval` | `5m` | Go duration, ≥ `1s` |
| Single run | — | `--once` | off | |
| HTTP timeout (per request) | `ASANA_HTTP_TIMEOUT` | `--http-timeout` | `30s` | > 0 |
| Request rate limit (requests/min) | `ASANA_RATE_LIMIT_PER_MINUTE` | `--rate-limit` | `60` | 1–1500 (Asana's paid-tier quota) |
| Max retries per request | `ASANA_MAX_RETRIES` | `--max-retries` | `5` | 0–20 |
| Log level | `LOG_LEVEL` | `--log-level` | `info` | debug/info/warn/error |
| API base URL (tests only) | `ASANA_BASE_URL` | — | `https://app.asana.com/api/1.0` | https, or http on loopback only |

The token has no flag on purpose: flags show up in `ps` output and shell history.

```sh
go run ./cmd/asana-extractor                   # every 5m
go run ./cmd/asana-extractor --interval=5m     # same, explicit
go run ./cmd/asana-extractor --interval=30s    # every 30s
go run ./cmd/asana-extractor --once            # one run, then exit
go run ./cmd/asana-extractor -h                # all flags
```

With `--once`, the interval is ignored (but still validated).

**Exit codes:** `0` success (or clean shutdown in periodic mode), `1` a `--once` run failed or was interrupted, `2` invalid configuration.

## 3. Output format

```
output/
  users.json       # JSON array of every visible user
  projects.json    # JSON array of every visible project (archived included)
```

Each file is one JSON array of resource objects (not the API envelope), in API order: users sorted by GID, projects in the order Asana lists them. An empty workspace produces `[]`.

`users.json`:

```json
[
  {
    "gid": "1200000000000101",
    "name": "Ann Example",
    "email": "ann@example.com"
  },
  {
    "gid": "1200000000000102",
    "name": "Bob Example",
    "email": null
  }
]
```

`projects.json`:

```json
[
  {
    "gid": "1200000000000201",
    "name": "Launch",
    "archived": false,
    "completed": false,
    "color": "light-green",
    "created_at": "2026-09-01T10:00:00.000Z",
    "modified_at": "2026-09-20T08:30:00.000Z",
    "due_on": null,
    "start_on": null,
    "privacy_setting": "public_to_workspace",
    "permalink_url": "https://app.asana.com/0/1200000000000201/1200000000000201",
    "owner": { "gid": "1200000000000101" },
    "team": { "gid": "1200000000000301", "name": "Engineering" },
    "workspace": { "gid": "1200000000000001", "name": "Test Workspace" }
  }
]
```

- **Fields** come from `opt_fields` on the list endpoints, so there are no per-resource detail requests. Users: `name,email`. Projects: `name,archived,completed,color,created_at,modified_at,due_on,start_on,privacy_setting,permalink_url,owner,team,team.name,workspace,workspace.name`. Only values listed in Asana's documented `opt_fields` enum are requested; e.g. `owner.name` is not in that list, so `owner` carries only its `gid` (confirmed on a live run); look the name up in `users.json` by `gid`.
- **Optional values** are written as `null` when Asana omits them or returns null (e.g. `email` hidden from the token's user, a project without an owner). Timestamps and dates are kept exactly as Asana returns them. GIDs are strings.
- **Archived projects are included.** The `archived` query parameter is not sent, so both archived and active projects are listed, and each object carries `"archived"`.
- **Writes are streamed and atomic.** Each page is appended to a temp file in the output directory as it arrives (memory stays bounded by one page, however large the workspace). Only after the **whole listing** of that resource type succeeds is the temp file `fsync`ed and renamed over `users.json` / `projects.json`. If the listing or the write fails, the temp file is removed and the previous file stays as it was. Temp files left by a hard crash (e.g. `SIGKILL`) and older than one minute are removed at the next startup. If the new content is identical to the file on disk, the file is not rewritten (`unchanged` in the logs).
- **Semantics:** each file is the **latest complete listing** of that resource type. Resources deleted in Asana, or no longer visible to the token, disappear from the array on the next successful run. The two files are published independently: if the users listing fails but the projects listing succeeds, `projects.json` is fresh and `users.json` keeps the previous run's listing. So the pair is **not a transactionally consistent snapshot**, and every run logs `status=success`, `status=failed` or `status=canceled`.

## 4. API usage, rate limiting and retries

Endpoints (both paginated with `limit=100`):

- `GET /users?workspace=<gid>&opt_fields=…` (not `/workspaces/<gid>/users`, which is capped at 2,000 users)
- `GET /projects?workspace=<gid>&opt_fields=…`

Pagination follows the opaque `next_page.offset` and stops only when `next_page` is `null`, never just because a page is short. Every page sends the same `workspace`, `opt_fields` and `limit`. Each page is written to disk before the next one is fetched, so memory use is bounded by one page.

**Pacing.** One pacer per process spaces request starts at least `60s / rate` apart. Every request goes through it, including retries. Requests are sequential: at this volume concurrency would mostly add 429 risk. The default of **60 requests/min** is 40% of Asana's documented free-tier quota (150/min; paid tiers get 1,500/min). That leaves room for other integrations using the same credentials, and matters because requests rejected by Asana still count against the quota. Asana also enforces concurrency limits (50 concurrent GETs) and a cost-based limit; both answer with 429, which is handled below.

**Retries** (at most `ASANA_MAX_RETRIES` after the first attempt):

| Condition | Retried? | Wait |
|---|---|---|
| `429` with valid `Retry-After` (seconds or HTTP-date) | yes | **exactly** `Retry-After`, never capped or shortened; set as a shared cooldown in the pacer so no request starts earlier, even after retries are exhausted (the next page, resource or run waits too) |
| `429` with missing / invalid `Retry-After` | yes | exponential backoff with jitter, also applied as a shared cooldown |
| `500`, `502`, `503`, `504` | yes | exponential backoff with jitter |
| transport errors (connection reset, timeout, DNS, …) | yes | exponential backoff with jitter |
| `400`, `401`, `402`, `403`, `404`, other 4xx, other 5xx (`501`, `505`, …), 3xx (redirects are not followed) | no | fails immediately with a hint (e.g. "check ASANA_ACCESS_TOKEN") |
| `200` with malformed JSON | no | fails immediately |

Backoff for retry *n* (0-based) is `d = min(1s·2ⁿ, 30s)`, then a random value in `[d/2, d]` ("equal jitter"). The 30s cap applies only to local backoff, never to a server-required `Retry-After`. Every wait (pacing, cooldown, backoff, in-flight request) stops as soon as the process context is canceled.

Errors name the operation and page (e.g. `list projects page 3: asana API returned 403: …`). They never contain request headers, so the token can't leak through them. Transport errors drop the URL query string. Redirects are not followed, so the `Authorization` header is only ever sent to the configured API host.

## 5. Scheduling and shutdown

- The first extraction runs **immediately** at startup.
- Later starts sit on a fixed grid `T0 + k·interval`, so short runs don't make the schedule drift.
- **At most one extraction runs at a time** (the scheduler calls it synchronously).
- Grid slots that pass while a run is still active are **skipped**, not queued. The next start is the first grid slot at or after the moment the run finished, so an overrun never triggers a catch-up burst. Skips are logged (`skipped=<n>`).

  Example, `--interval=30s`, first run takes 70s: starts happen at 0s, 90s, 120s… The slots at 30s and 60s are skipped.
- In periodic mode a failed run is logged (`level=ERROR … status=failed`) and the next slot runs normally.
- `SIGINT`/`SIGTERM` cancel in-flight HTTP requests, retry waits and the wait for the next slot. An interrupted run is logged as `status=canceled`, and the process exits `0` in periodic mode or `1` for `--once`. A second signal terminates the process immediately (default signal handling is restored after the first).

Log lines are structured (`log/slog` text format on stderr), e.g.:

```
level=INFO msg="extraction finished" run=3 duration=1.27s users=250 projects=40 written=2 unchanged=288 retries=0 status=success
```

Tokens, headers and resource payloads (names, emails) are never logged.

## 6. Scale analysis

Assumptions: page size 100, one request per page, **no per-resource detail requests** (list responses carry every exported field), no failures or retries.

```
requests per run = ceil(users / 100) + ceil(projects / 100)
```

| Workspace | Requests/run | Needed at 5m | Needed at 30s |
|---|---|---|---|
| 250 users, 40 projects | 3 + 1 = 4 | 0.8 req/min | 8 req/min |
| 5,000 users, 3,000 projects | 50 + 30 = **80** | **16 req/min** | **160 req/min** |
| 20,000 users, 10,000 projects | 200 + 100 = 300 | 60 req/min | 600 req/min |

Worked example, 5,000 users and 3,000 projects:

- **Every 5 minutes:** 80 requests, 16 req/min on average. The default pacing (1 request/s) finishes a run in about 80s plus latency, well within the 5-minute interval and 11% of the free-tier quota.
- **Every 30 seconds:** 160 req/min would be needed. That's **more than the free-tier quota** (150/min) and more than the default pacing (60/min). At the default pacing a run takes at least ~80s, so the 30s slots in between are skipped and data actually refreshes about every 90s. Refreshing within 30s would need a paid tier and a raised limit (e.g. `--rate-limit=600`, 40% of the paid quota), and even then total per-request latency must stay under 30s: 80 requests × ~0.3s is about 24s, which leaves little margin.

So a 30-second schedule **can't guarantee complete, fresh data every 30 seconds**. When `requests/run × max(spacing, latency)` exceeds the interval, or retries/429 cooldowns stretch a run, slots are skipped and the output is as fresh as the last completed run. Within one file, entries from early pages were fetched earlier than entries from late pages, so a long listing is not a point-in-time view.

### Beyond this exercise (discussion only; not implemented)

- **Incremental extraction.** Checked against the docs: `GET /users` and `GET /projects` have **no `modified_since`-style filter**, so every run is a full listing. Pagination offsets are temporary tokens that expire; they are not sync checkpoints and are never persisted.
- **Events and webhooks.** Workspace-level webhooks support filters for *Project* (`added`, `removed`, `deleted`, `undeleted`, `changed`) and *Workspace Membership* (`added`, `removed`), but there are **no events for user profile changes** (name, email). The workspace events API (`GET /workspaces/{gid}/events`) is available only to a service account in an Enterprise+ domain, and its sync token expires after 4 hours of inactivity. A realistic design would be webhooks for project changes and membership adds/removes, plus a **periodic full reconciliation** (e.g. hourly or nightly) that re-lists everything, catches missed deliveries and user profile changes, and detects deletions.
- **Multiple instances.** Quotas belong to the credentials, not the process. Several instances would need one shared budget: a single leader via a lease or lock, or a shared token bucket (e.g. in Redis), plus a shared 429 cooldown and staggered schedules. Otherwise N instances use N× the requests and hit 429s together. Two instances writing to the same directory never produce truncated files (atomic rename), but they waste quota and race on the same files; run one instance per output directory.
- **Storage at scale.** A single array file per type is simple to ship but must be re-read in full by consumers and is rewritten on any change (tens of MB for 100k resources). At larger scale, write to a database or object store keyed by GID, with `last_seen_run` and `last_seen_at` columns; a successful full run can then mark resources not seen as deleted or inaccessible (tombstones), and consumers can pick a consistent run ID across users and projects.

## 7. Known limitations

- `users.json` and `projects.json` are each a complete listing, but they are published independently, so together they are not a consistent snapshot; a failed listing leaves the previous file in place until the next successful run.
- Only resources visible to the PAT's user are exported. `email` can be `null`, and guests or pending invitees may be missing or partial. How the API treats pending invitations wasn't verified live here (see §1 step 5).
- Asana documents that `GET /projects` "may timeout for large domains". The suggested `team` filter is deprecated, so this tool relies on pagination plus retries. A persistent timeout fails the run.
- A very long `Retry-After` mid-listing can outlive the pagination offset. The next page then fails with `400`, the run fails, and the next run starts over.
- Rate limiting is per process (see *Multiple instances*).
- Each changed file is `fsync`ed, but the directory is not, so after a power loss a renamed file may revert to its previous complete version on some filesystems. It is never left truncated.
- No live Asana verification was run while building this: all tests use a mock server. Run `make live-test` with your credentials.

## 8. Development and tests

Layout:

```
cmd/asana-extractor/   main: config → wiring → once or scheduler; exit codes; offline E2E tests
internal/config/       flags, env, .env parsing, validation
internal/asana/        HTTP client: auth, pagination, pacer, retries, typed errors
internal/storage/      streamed, atomic JSON array file per resource type
internal/extractor/    one extraction run (users, then projects) + run stats/logging
internal/scheduler/    immediate + fixed-grid scheduling with skip-on-overrun
internal/asanatest/    in-memory fake Asana API for tests
```

Commands:

```sh
gofmt -l .                # formatting check (no output = clean); `make fmt` rewrites
go vet ./...
go test ./...
go test -race ./...
go build ./...
make check                # all of the above

go test ./internal/scheduler -run TestOverrun -v            # a single test
make live-test            # optional: real API, needs ASANA_ACCESS_TOKEN + ASANA_WORKSPACE_GID (env or .env)
```

The tests are deterministic. Scheduling and retry timing use an injected virtual clock, and the only real waits are short ones (≤ ~1.5s) that exercise cancellation and the end-to-end periodic run. The live test is skipped unless `ASANA_LIVE_TEST=1`.

## Sources

- Asana API docs, via Context7 and, where Context7 lacked details, fetched directly: [Pagination](https://developers.asana.com/docs/pagination), [Rate limits](https://developers.asana.com/docs/rate-limits), [Get multiple users](https://developers.asana.com/reference/getusers), [Get users in a workspace](https://developers.asana.com/reference/getusersforworkspace), [Get multiple projects](https://developers.asana.com/reference/getprojects), [Input/output options](https://developers.asana.com/docs/inputoutput-options), [Webhooks guide](https://developers.asana.com/docs/webhooks-guide), [Workspace events](https://developers.asana.com/docs/workspace-events), [Personal access token](https://developers.asana.com/docs/personal-access-token).
- Research notes: `.ai-factory/research/asana-extractor-exercise/RESEARCH.md`.

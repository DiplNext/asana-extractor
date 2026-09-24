---
name: asana-api
description: >-
  Asana REST API v1.0 reference for building a Go stdlib client that extracts users and projects:
  PAT auth, workspace discovery, offset-token pagination, opt_fields, 429/Retry-After handling,
  concurrency caps and error decoding. Use when writing or reviewing code that calls
  app.asana.com/api/1.0, handles Asana pagination or rate limits, or maps Asana JSON to Go types.
user-invocable: false
metadata:
  version: "1.0"
  category: api-reference
---

# Asana API (users & projects extraction)

Base URL: `https://app.asana.com/api/1.0`. All responses are JSON wrapped in `{"data": ...}`.

## Auth

- Personal Access Token: `Authorization: Bearer <token>`, read from `ASANA_ACCESS_TOKEN` env.
- Sanity check: `GET /users/me` → returns the token's user plus `workspaces[]`.
- Never log the token or the `Authorization` header; redact it in request dumps.

## Endpoints used

| Purpose | Request | Notes |
|---|---|---|
| Workspaces | `GET /workspaces` | paginated; `gid`, `name`, `is_organization` |
| Users | `GET /users?workspace=<gid>` | also accepts `team=<gid>`; one of the two filters is needed |
| Projects | `GET /projects?workspace=<gid>` | optional `team=<gid>`, `archived=true|false`; omit `archived` to get both |

Every object has a string `gid` (not an int — never parse it as a number) and `resource_type`.

## Pagination

- Send `limit` (1–100). Pagination is only active when `limit` is set — **always send `limit=100`**.
- Response: `{"data": [...], "next_page": {"offset": "...", "path": "...", "uri": "..."} | null}`.
- Loop: pass `next_page.offset` as `offset=` on the next request with identical other params; stop when `next_page` is `null`.
- Offset tokens expire — don't persist them for later resume; restart the listing instead.

## opt_fields

- Compact records are returned by default (`gid`, `name`, `resource_type`). Request extra fields explicitly:
  - users: `opt_fields=name,email,photo,workspaces.name`
  - projects: `opt_fields=name,archived,color,created_at,modified_at,owner,team.name,workspace.name,public,notes,permalink_url`
- Dotted paths expand nested objects (e.g. `team.name`), but request only values listed in the endpoint's documented `opt_fields` enum (`/projects` lists `team.name`, `workspace.name`, but not `owner.name`). Fewer fields = faster, cheaper calls.
- `opt_pretty=true` only for debugging.

## Rate limits

- Per-minute quota per token: ~150 req/min on free domains, ~1500 on paid.
- Separate concurrent-request limits for GET vs writes (GET cap is 50 in-flight). This project issues requests sequentially through one shared pacer.
- Exceeded → `429` with `Retry-After: <seconds>`. Wait at least that long (respect `ctx`) as a shared cooldown, then retry; never cap or shorten a server-required delay. 429 retries count toward the bounded retry budget. Rejected requests still count against the quota.
- Retry `500/502/503/504` with exponential backoff + jitter (3–5 attempts). Never retry `400/401/402/403/404`.

## Errors

Body: `{"errors": [{"message": "...", "help": "...", "phrase": "..."}]}` (`phrase` only on 500s — log it, support uses it).

| Status | Meaning | Action |
|---|---|---|
| 400 | bad params | fail fast, surface `message` |
| 401 | bad/missing token | abort whole run |
| 402 | premium-only feature | skip + warn |
| 403 | no access to object | skip + warn, continue |
| 404 | object missing | skip + warn |
| 429 | rate limited | wait `Retry-After`, retry |
| 5xx | server | backoff retry |

## Go implementation

Stdlib-only client patterns (generic paginator, retry transport, typed API error, models, atomic JSON writer): see [references/GO-CLIENT.md](references/GO-CLIENT.md).

## Pitfalls

- Forgetting `limit` → no `next_page`, and large workspaces can reject the unpaginated request.
- Treating `gid` as int64 → overflow/precision bugs in other languages; keep `string`.
- Users listed per workspace overlap across workspaces — dedupe by `gid` if merging.
- Guests in organizations appear in `/users`; filter by email domain if only members are wanted.
- Reusing an old offset token after a long pause → 400; restart from the first page.

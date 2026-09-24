# asana-extractor

## Overview

Go CLI that extracts Asana **users** and **projects** via the Asana REST API (`https://app.asana.com/api/1.0`) and writes them to JSON files. Stdlib only — no CLI framework, no Asana SDK.

## Core Features

- Extract users of the configured workspace (`GET /users?workspace=<gid>`)
- Extract projects (archived included) of that workspace (`GET /projects?workspace=<gid>`)
- Follow cursor pagination (`limit=100`, opaque `next_page.offset`) until `next_page` is null
- One JSON array per resource type: `output/users.json`, `output/projects.json`, streamed and atomically replaced
- Run every 5m (default), every 30s, or once (`--once`)
- Sequential paced requests; honor `429 Retry-After`; bounded jittered retries for 5xx/transport errors

## Tech Stack

- **Programming language:** Go (go.mod `go 1.24`; developed with 1.27)
- **Framework:** none — stdlib `flag`, `net/http`, `encoding/json`, `log/slog`, `context`
- **Database:** none — output is JSON files
- **ORM:** n/a
- **Integrations:** Asana REST API v1.0, Personal Access Token auth (`Authorization: Bearer $ASANA_ACCESS_TOKEN`)

## Architecture Notes

- Thin API client layer (HTTP + pagination + retry) separated from extraction logic and from output writers.
- All network calls take a `context.Context`; SIGINT cancels in-flight work.
- Pagination is generic (`[T any]`) with a per-page callback so users/projects share one implementation and memory stays bounded.
- Storage writes a temp file in the destination dir and `os.Rename`s it into place, so an interrupted run never leaves a half-written JSON file.
- Use `opt_fields` to request only needed fields and shrink payloads.

## Non-Functional Requirements

- Logging: `log/slog`, level configurable via `LOG_LEVEL`
- Error handling: wrap errors with `%w` and context; decode Asana `errors[].message` into typed errors
- Security: token read from env only, never logged or written to output; HTTP client with explicit timeouts

## Architecture

See `.ai-factory/ARCHITECTURE.md` for detailed architecture guidelines.
Pattern: Layered (cmd → extractor → asana / storage; scheduler, config)

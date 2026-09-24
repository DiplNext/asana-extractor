# AGENTS.md

> Structural map for AI agents. Keep it factual; update when project structure changes.

## Project Overview

Go CLI that extracts Asana users and projects via the REST API into JSON files. See `.ai-factory/DESCRIPTION.md`.

## Tech Stack

- **Programming language:** Go (go.mod `go 1.24`)
- **Framework:** none (stdlib only)
- **Database:** none (JSON file output)
- **ORM:** n/a

## Project Structure

```
.
├── cmd/asana-extractor/   # CLI entry point, wiring, exit codes, offline E2E tests
├── internal/
│   ├── asana/             # Asana HTTP client: pagination, pacer, retries, errors
│   ├── asanatest/         # fake Asana API for tests
│   ├── config/            # flags / env / .env, validation
│   ├── extractor/         # one extraction run
│   ├── scheduler/         # periodic scheduling
│   └── storage/           # atomic per-resource JSON files
├── .ai-factory/           # AI Factory config, description, architecture, rules, research
├── .claude/               # Claude Code skills (aif-*, asana-api) and agents
├── .env.example           # configuration template (copy to .env)
├── Makefile               # build / run / check targets
├── PLAN.md                # implementation plan + checklist
└── README.md              # setup, configuration, behavior, scale analysis
```

## Key Entry Points

| File | Purpose |
|------|---------|
| `cmd/asana-extractor/main.go` | `run()` — config, wiring, once vs periodic |
| `internal/asana/client.go` | HTTP client, pagination, retry policy |
| `internal/scheduler/scheduler.go` | scheduling semantics |
| `internal/storage/storage.go` | atomic file writes |
| `.ai-factory/config.yaml` | AI Factory settings |

## Documentation

| Document | Path | Description |
|----------|------|-------------|
| README | README.md | Setup, configuration, behavior, scale analysis, test commands |
| Plan | PLAN.md | Implementation checklist with acceptance criteria |

## AI Context Files

| File | Purpose |
|------|---------|
| AGENTS.md | Project structure map |
| .ai-factory/DESCRIPTION.md | Project specification and tech stack |
| .ai-factory/ARCHITECTURE.md | Architecture decisions and guidelines |
| CLAUDE.md | Claude Code instructions |

## Agent Rules

- Never chain shell commands with `&&` when each step can fail independently — run them as separate commands.
  - Wrong: `git checkout main && git pull`
  - Right: first `git checkout main`, then `git pull origin main`

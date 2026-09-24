# Project Base Rules

> No code exists yet — these are baseline conventions for the chosen stack (Go, stdlib only). Update once real patterns emerge.

## Naming Conventions

- Files: `snake_case.go`, tests `*_test.go` next to the code
- Variables: `camelCase`; short names in small scopes (`ctx`, `err`, `req`)
- Functions: `camelCase` unexported, `PascalCase` exported; no `Get` prefix on getters
- Types: `PascalCase`; initialisms stay uppercase (`GID`, `HTTPClient`, `URL`)
- Packages: short, lowercase, single word, no `util`/`common`

## Module Structure

- `cmd/asana-extractor/` — `main`, flag parsing, wiring only
- `internal/` — all non-main code; nothing exported outside the module

## Error Handling

- Return errors, don't panic; wrap with `fmt.Errorf("fetch users: %w", err)`
- Sentinel/typed errors for API failures, checked with `errors.Is` / `errors.As`
- Only `main` decides exit codes and prints to stderr

## Control Flow

- Prefer flat, readable control flow over deeply nested conditionals. Use guard clauses, early `return`/`continue`, small named helper methods, or explicit classification logic when they make the code easier to follow. Handle edge cases and irrelevant branches early so the main path stays visible.

## Logging

- `log/slog` only, structured key/value attrs; level from `LOG_LEVEL`
- Never log the Asana token or `Authorization` header

## Dependencies

- Stdlib only unless explicitly approved
- Tests: stdlib `testing` + `net/http/httptest` for API fakes; table-driven

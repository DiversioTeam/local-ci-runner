# AGENTS.md

Minimal TOC for agent readers.

## Read order

1. `README.md`
2. `docs/architecture.md`
3. `docs/contracts.md`
4. `docs/inspection.md`
5. `cmd/local-ci/MANUAL.md` when you need the full CLI/help surface

## Repo rules

- Go-first repo.
- Keep repo-specific CI logic out of the runner.
- No relative/local imports; keep the internal package graph acyclic.
- Persist typed contracts, not `map[string]any`.
- CLI is a view over the engine, not a second execution path.
- Fail closed for repo identity, SHA, config hash, and plan hash mismatches.
- Third-party auth env vars exposed by this tool must use `LOCAL_CI_` prefixes.

## Checks

```bash
gofmt -w cmd internal
go test ./...
go vet ./...
ruff check .
```

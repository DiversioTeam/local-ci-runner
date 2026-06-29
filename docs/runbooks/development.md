# Development runbook

Short everyday loop for contributors working on `local-ci-runner`.

## Fast path

```bash
gofmt -w cmd internal
go test ./...
go vet ./...
ruff check .
go build ./cmd/local-ci
go run ./cmd/local-ci --help
```

If you changed the built-in docs or operator UX, also run:

```bash
go run ./cmd/local-ci manual
```

## Where to start reading

- config/planner shape -> `docs/contracts.md`
- engine boundary / persisted state -> `docs/architecture.md`
- inspection/read-back behavior -> `docs/inspection.md`
- full CLI surface -> `cmd/local-ci/MANUAL.md`

## Common work areas

- `cmd/local-ci` — command wiring and manual/help surface
- `internal/config` — TOML loading and validation
- `internal/planner` — planner invocation contract
- `internal/engine` — run lifecycle, publish, resume, reporting
- `internal/persistence` — run directory layout and summary files
- `internal/github` — commit-status posting
- `internal/update` — version/update notice behavior

## Example config

Use the static example when you need a minimal consumer-repo config:

```text
examples/basic/.local-ci.toml
```

## Release helper flow

Before cutting a release tag, make sure local gates are already green.

Relevant files:
- `.github/workflows/release.yml`
- `scripts/release/write-homebrew-formula.sh`

Mental model:

```text
tag vX.Y.Z
  -> GitHub Actions verifies gofmt/test/vet
  -> builds archives
  -> publishes GitHub release
  -> rewrites Homebrew formula
  -> pushes tap update when token exists
```

## Golden rules

- Keep the runner generic; consumer repos own the checks.
- Keep contracts typed and explicit.
- Treat persisted run files as the execution/read-back contract.
- Keep resume/publish safety fail-closed.

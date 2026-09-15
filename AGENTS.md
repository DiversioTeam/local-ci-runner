# AGENTS.md

Canonical entrypoint for agent work in `local-ci-runner`.

## What this repo is

This repo owns the shared `local-ci` runner.

It is responsible for:
- loading `.local-ci.toml`
- optionally asking a repo-owned planner for a resolved plan
- executing black-box steps
- persisting runs under `.local-ci/runs/<run-id>/`
- exposing read-back commands over those persisted artifacts
- posting GitHub commit statuses for the exact verified snapshot

It is **not** responsible for Django, pytest, npm, CircleCI, or any other
consumer-repo-specific workflow logic.

## Docs, in read order

1. `README.md` — what the tool is and how to install it
2. `docs/README.md` — docs index and routing
3. `docs/contracts.md` — config, planner, run-artifact, event, and GitHub-posting contracts
4. `docs/architecture.md` — write path, read path, persistence, resume safety
5. `docs/inspection.md` — operator mental model for `runs`, `show`, `logs`, `publish`
6. `docs/quality/gates.md` — local commands, release CI, and common failures
7. `docs/runbooks/development.md` — everyday dev loop and release-helper workflow
8. `cmd/local-ci/MANUAL.md` — full CLI/help surface, when you need it

## Repo shape

- `cmd/local-ci` — CLI entrypoint and built-in manual surface
- `internal/config` — `.local-ci.toml` loading and validation
- `internal/planner` — planner contract execution
- `internal/engine` — run orchestration, resume safety, publish checks
- `internal/persistence` — on-disk run layout
- `internal/events` — append-only event log contract
- `internal/github` — commit-status posting
- `internal/gitrepo` — repo discovery and identity helpers
- `internal/update` — release version, update notice, and self-update logic
- `.local-ci.toml` — this repo's own verification steps
- `examples/basic` — minimal static config example
- `.github/workflows/release.yml` — tagged release pipeline
- `scripts/install.sh` — curl-to-shell installer for macOS and Linux
- `scripts/release/write-homebrew-formula.sh` — Homebrew formula generator

## Commands

```bash
gofmt -w cmd internal
go test ./...
go vet ./...
go build ./cmd/local-ci
go run ./cmd/local-ci --help
go run ./cmd/local-ci manual
```

This repo verifies itself with its own runner. From a clean worktree:

```bash
local-ci run
```

## Non-negotiable rules

- Keep repo-specific CI logic out of the runner; consumer repos own checks and planners.
- No relative/local imports; keep the internal package graph acyclic.
- Persist typed contracts, not `map[string]any`.
- The engine is the source of truth; the CLI is a thin operator surface, not a second execution path.
- Inspection commands read persisted files; they do not attach to a running process.
- Resume/publish must fail closed for repo identity, SHA, config hash, plan hash, and snapshot mismatches.
- Third-party auth env vars exposed by this tool must use `LOCAL_CI_` prefixes.
- Preserve the clean split between generic runner behavior and repo-owned planner behavior.

## Release notes

- Tags use `vX.Y.Z` format.
- Release automation lives in `.github/workflows/release.yml`.
- Homebrew formula generation lives in `scripts/release/write-homebrew-formula.sh`.
- The tap update step requires `HOMEBREW_TAP_TOKEN`.

# Quality gates

Use the repo's actual commands, not guessed wrappers.

## Required local commands

Run these before pushing:

```bash
gofmt -w cmd internal
go test ./...
go vet ./...
ruff check .
```

Recommended quick sanity checks while editing CLI behavior:

```bash
go build ./cmd/local-ci
go run ./cmd/local-ci --help
go run ./cmd/local-ci manual
```

## Why these gates exist

- `gofmt` keeps the Go tree mechanically clean.
- `go test ./...` is the main correctness gate.
- `go vet ./...` catches suspicious Go patterns.
- `ruff check .` is kept in the standard command set so future Python helper
  files stay linted; today it exits cleanly because the repo has no tracked
  Python files.

## Release CI

Tagged releases (`v*`) run `.github/workflows/release.yml`.

That workflow currently enforces:

```bash
test -z "$(gofmt -l cmd internal)"
go test ./...
go vet ./...
```

Then it:
- builds release archives for darwin/linux amd64/arm64
- publishes the GitHub release
- regenerates the Homebrew formula with `scripts/release/write-homebrew-formula.sh`
- pushes the tap update when `HOMEBREW_TAP_TOKEN` is configured

## Common failures

- Formatting drift in `cmd/` or `internal/`
- Re-introducing repo-specific workflow knowledge into the generic runner
- Contract drift between `.local-ci.toml`, planner stdout, and persisted artifacts
- Breaking fail-closed publish/resume checks for repo identity, SHA, config hash, or plan hash
- Using generic auth env vars instead of the repo's `LOCAL_CI_`-prefixed contract

## Review discipline

If a bug repeats, prefer a harness fix:
- document the rule here or in another focused doc
- add or tighten a test
- improve the CLI/manual error message
- keep the engine/CLI boundary explicit instead of patching around it in prose

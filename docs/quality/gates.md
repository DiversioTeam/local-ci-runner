# Quality gates

Use the repo's actual commands, not guessed wrappers.

## Required local commands

Run these before pushing:

```bash
gofmt -w cmd internal
go test ./...
go test -race ./...
go vet ./...
```

Recommended quick sanity checks while editing CLI behavior:

```bash
go build ./cmd/local-ci
go run ./cmd/local-ci --help
go run ./cmd/local-ci manual
```

## Why these gates exist

- `gofmt` keeps the Go tree mechanically clean.
- `go test ./...` is the main correctness gate. `go test -race ./...` also
  checks the single-writer scheduler and every worker handoff.
- Binary-only tests build an executable with an injected version and run it
  outside a repo without Git on PATH, so help, the manual, version and exit
  status are checked as operators get them.
- The log write-failure row needs `/dev/full`, so it runs on Linux (release CI)
  and skips on macOS.
- Which test owns which contract, and how to add one, is in
  [`testing.md`](./testing.md).
- `go vet ./...` catches suspicious Go patterns.

## Release CI

Tagged releases (`v*`) run `.github/workflows/release.yml`.

That workflow currently enforces:

```bash
test -z "$(gofmt -l cmd internal)"
go test ./...
go test -race ./...
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

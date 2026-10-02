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
- `go test ./...` is the main correctness gate. Publication tests cover
  exact targets, durable intent before posting, persistence failures, retries,
  unknown reporting errors, and refusal to append to torn event logs.
- Binary-only documentation tests build an executable with an injected version,
  then run help/manual/version outside a repo without Git on PATH.
  They verify embedded schema documentation against the compiled contract.
  CLI tests verify existing runner-log JSON exposes receipt fields without
  mutating inspected artifacts.
- `go test -race ./...` covers worker limits, ready-step ordering, skipped dependencies, sibling failures, shared GitHub contexts, timeout/interruption, resume/rerun, and isolated logs. Worker limits and dependency order are read back from the persisted event sequence, not from timing. Fatal persistence errors are proven to cancel and join every worker. The log write-failure row needs `/dev/full`, so it runs on Linux, including release CI, and skips on macOS.
- CLI tests verify plan preview creates no run/executes no steps, rejects planner changes to HEAD/source/config, execution JSON never mixes child output, and unsuccessful results remain inspectable. Reporting tests cover aggregate-context collisions, fatal receipt writes during cancellation, and immediate failure-log progress before remote posts. Compiled-binary tests cover discovery without source or Git.
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

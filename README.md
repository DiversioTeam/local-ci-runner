<p align="center">
  <img src="./assets/local-ci-logo.png" alt="local-ci logo" width="240" />
</p>

# local-ci-runner

Shared local CI runner for repo-owned verification steps.

`local-ci` lets a repository define its own verification plan, run that exact
plan on a developer machine, keep the full run artifacts local, and post a
GitHub status only when the verified snapshot still matches the commit being
reported.

## Install

Release binaries are published for macOS and Linux on both `amd64` and
`arm64`.

### Install script (macOS, Debian, Ubuntu, Arch, and others)

```bash
curl -fsSL https://raw.githubusercontent.com/DiversioTeam/local-ci-runner/main/scripts/install.sh | sh
```

The script detects your OS and architecture, verifies the release checksum,
and installs to `~/.local/bin`. If you would rather
read it before running it — a good habit for any piped installer:

```bash
curl -fsSL https://raw.githubusercontent.com/DiversioTeam/local-ci-runner/main/scripts/install.sh -o install-local-ci.sh
less install-local-ci.sh
sh install-local-ci.sh
```

Two environment variables adjust what it does:

| Variable | Default | Purpose |
| --- | --- | --- |
| `LOCAL_CI_VERSION` | latest release | Install a specific version, e.g. `0.2.0` |
| `LOCAL_CI_INSTALL_DIR` | `$HOME/.local/bin` | Install somewhere else |

```bash
# Pin a specific version.
curl -fsSL https://raw.githubusercontent.com/DiversioTeam/local-ci-runner/main/scripts/install.sh \
  | LOCAL_CI_VERSION=0.2.0 sh

# Install system-wide instead of per-user.
curl -fsSL https://raw.githubusercontent.com/DiversioTeam/local-ci-runner/main/scripts/install.sh -o install-local-ci.sh
sudo sh -c 'LOCAL_CI_INSTALL_DIR=/usr/local/bin sh install-local-ci.sh'
```

It needs `tar`, a SHA-256 tool, and either `curl` or `wget` — all present on
a stock Debian, Ubuntu, Arch, or macOS system. If `~/.local/bin` is not on
your `PATH` the script tells you what to add to your shell profile.

### Upgrading

However you installed it:

```bash
local-ci update
```

### Homebrew (macOS and Linux)

```bash
brew tap DiversioTeam/tap
brew install local-ci
```

Upgrade later with:

```bash
brew update
brew upgrade local-ci
```

### Manual download

Every release attaches `local-ci_<version>_<os>_<arch>.tar.gz` plus a
`checksums.txt`, so you can pull a tarball straight from the
[releases page](https://github.com/DiversioTeam/local-ci-runner/releases),
verify it, and drop the `local-ci` binary anywhere on your `PATH`.

### From source

Any platform with a Go toolchain matching `go.mod`:

```bash
go install github.com/DiversioTeam/local-ci-runner/cmd/local-ci@latest
```

This builds from source and does not stamp a release version, so
`local-ci version` reports a development version and the update check stays
quiet.

### Prerequisites

`local-ci` shells out to `git`, so install it if your machine does not have
it already — `sudo apt install git` on Debian and Ubuntu, `sudo pacman -S
git` on Arch. Posting GitHub commit statuses also needs the `gh` CLI
([installation instructions](https://github.com/cli/cli#installation)).

<p align="center">
  <img src="./assets/local-ci-overview.gif" alt="local-ci runs the repo’s exact plan on your machine. Inspect logs, artifacts, and events locally. Optional GitHub posting requires a matching repo, SHA, config, plan, and tree; report the verified pass or fail result." width="1100" />
</p>

[View the static diagram](./assets/local-ci-overview.png).

## What this is

`local-ci` is the runner, not the checks.

It:
- loads `.local-ci.toml`
- optionally asks a repo-owned planner for a resolved plan
- executes black-box steps
- writes run artifacts under `.local-ci/runs/<run-id>/`
- optionally posts GitHub commit statuses for the verified snapshot

It does **not** know Django, pytest, npm, CircleCI, or any other
consumer-repo-specific workflow.

## Quickstart

Minimal static config:

```toml
version = 1

[github]
enabled = true
aggregate_context = "local/verify"

[[steps]]
id = "test"
command = ["go", "test", "./..."]
```

Run it:

```bash
local-ci run
local-ci runs
local-ci show <run-id>
local-ci logs <run-id>
```

Dirty worktree while iterating:

```bash
local-ci run --no-github
```

SIGINT or SIGTERM stops the active step—including its process group on macOS
and Linux—persists the run as `interrupted`, and lets
`local-ci resume <run-id>` rerun unfinished work.

A GitHub status-post failure is recorded and disables further posting without
stopping local checks. Fix auth or connectivity, then publish the completed run.

## Command map

```bash
local-ci run
local-ci resume <run-id>
local-ci runs
local-ci show <run-id>
local-ci logs <run-id>
local-ci logs <run-id> --step <step-id>
local-ci publish <run-id>
local-ci version
local-ci manual
```

## Binary-only discovery and publication evidence

No source checkout is needed to learn the installed version:

```bash
local-ci version
local-ci help version
local-ci manual         # embedded, version-matched reference
local-ci help show
local-ci logs <run-id> --runner --json
```

Help/manual/version and inspection commands do not contact GitHub or check for
updates. Existing runner logs include typed publication request/outcome events
with exact repository/SHA/context and timestamps. There is no separate summary
or inferred publication state. Missing legacy evidence remains unknown.
Receipts describe history, not current GitHub status or checkout validation.

`run`/`resume` execute repo-owned code and may post statuses. `publish` can
execute the planner and posts statuses; it is not inspection or a dry run.
These require separate authorization. See embedded manual section 8.1 for
receipt fields, failure handling, compatibility, and retry boundaries.

## Docs

- `AGENTS.md` — short agent/worktree map
- `docs/README.md` — docs index
- `docs/contracts.md` — `.local-ci.toml`, planner, run artifact, and status contracts
- `docs/architecture.md` — engine/write/read path model
- `docs/inspection.md` — `runs`, `show`, `logs`, `publish`
- `docs/quality/gates.md` — local checks and release CI
- `docs/runbooks/development.md` — contributor loop
- `cmd/local-ci/MANUAL.md` — long-form CLI manual

## Development

```bash
gofmt -w cmd internal
go test ./...
go vet ./...
go build ./cmd/local-ci
go run ./cmd/local-ci --help
go run ./cmd/local-ci manual
```

Use `examples/basic/.local-ci.toml` as the smallest repo config example.

## License

MIT — see [`LICENSE`](./LICENSE).

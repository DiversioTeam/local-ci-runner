# local-ci-runner

Shared local CI runner for repo-owned verification steps.

## Scope

This repo is the runner, not the checks themselves.

It is responsible for:
- loading config
- optionally asking a repo-owned planner for a resolved plan
- executing black-box steps
- persisting run state and logs under `.local-ci/runs/<run-id>/`
- posting GitHub commit statuses for the current repo HEAD SHA
- exposing the engine through a plain CLI

It is **not** responsible for knowing about Django, pytest, npm, CircleCI, or any other repo-specific workflow.

## Architecture

Layers:
1. `internal/config`: load and validate `.local-ci.toml`
2. `internal/planner`: optional planner contract; planner stdout resolves to a run plan
3. `internal/engine`: run model and step state machine
4. `internal/persistence`: file layout under `.local-ci/runs/<run-id>/`
5. `internal/github`: commit status posting contract
6. `internal/events`: append-only event schema
7. `cmd/local-ci`: operator entrypoint

The engine is the source of truth. The CLI is a thin operator surface.

## Contracts

### Config file

Default config path: `.local-ci.toml`

Static example:

```toml
version = 1

[github]
enabled = true
aggregate_context = "local/verify"

[[steps]]
id = "fmt"
command = ["go", "fmt", "./..."]
github_context = "local/fmt"

[[steps]]
id = "test"
command = ["go", "test", "./..."]
needs = ["fmt"]
github_context = "local/test"
```

### Planner contract

If `[planner].command` is set, the runner executes that command once before step execution.

Planner stdout must be JSON with this shape:

```json
{
  "env": {"CHANGED_SCOPE": "python"},
  "steps": [
    {
      "id": "lint",
      "command": ["./scripts/lint.sh"],
      "needs": [],
      "github_context": "local/lint"
    }
  ]
}
```

If no planner is configured, the static `[[steps]]` list becomes the plan.

v1 rule: use either `[planner]` or `[[steps]]`, not both.

### Engine-provided environment

Common env for planners and steps:
- `LOCAL_CI_RUN_ID`
- `LOCAL_CI_REPO_ROOT`
- `LOCAL_CI_CONFIG`
- `LOCAL_CI_RUN_DIR`
- `LOCAL_CI_PLAN_FILE`
- `LOCAL_CI_PLAN_ENV`
- `LOCAL_CI_GITHUB_REPO`
- `LOCAL_CI_GITHUB_SHA`

Step-only env:
- `LOCAL_CI_STEP_ID`
- `LOCAL_CI_STEP_NAME`
- `LOCAL_CI_STEP_INDEX`
- `LOCAL_CI_STEP_DIR`
- `LOCAL_CI_STEP_OUTPUT`

A step may write `KEY=VALUE` lines to `LOCAL_CI_STEP_OUTPUT` using normal environment-variable keys (`[A-Za-z_][A-Za-z0-9_]*`). v1 persists that file but does not treat it as a hidden data bus.

## Run artifact layout

```text
.local-ci/runs/<run-id>/
  meta.json
  plan.json
  plan.env
  summary.json
  summary.txt
  events.jsonl
  planner.log
  steps/
    001-<step>/
      status.json
      stdout.log
      stderr.log
      combined.log
      output.env
```

## Non-goals for v1

- no repo-specific built-ins
- no matrix workflows
- no retry DSL
- no database
- no alternate UI execution path

## GitHub auth for status posting

Preferred runner-specific env var:

```bash
export LOCAL_CI_GITHUB_TOKEN=github_pat_...
```

The runner will map `LOCAL_CI_GITHUB_TOKEN` to `GH_TOKEN`/`GITHUB_TOKEN` only for the `gh api` subprocess. This keeps the tool on a unique prefix instead of asking developers to export generic GitHub auth vars globally.

The runner intentionally strips inherited `GH_TOKEN` and `GITHUB_TOKEN` from that subprocess. If `LOCAL_CI_GITHUB_TOKEN` is unset, it falls back to the developer's existing `gh auth login` session instead of silently reusing generic GitHub token env vars.

### Getting a token

Preferred: a fine-grained personal access token for the target repo with:
- repository access to the repo you will post statuses to
- repository permission: **Commit statuses** = **Read and write**

Acceptable fallback: a classic token with `repo:status`.

## Usage

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

Flags:
- `--config <path>` on `run` and `resume`
- `--no-github` on `run` and `resume`
- `--json` on `runs`, `show`, and `logs`
- `--version` as a top-level shortcut

### Quick operator flow

Think about the tool in two halves:

```text
local-ci run / resume     -> write a run directory
local-ci runs / show / logs -> read that run directory
```

That split is deliberate.

Why:
- the engine gets one job: execute steps and persist facts
- the read-back commands get one job: explain those persisted facts clearly
- active-run inspection works from another shell because readers do not need to attach to the running process

Common flow:

```bash
local-ci run
local-ci runs
local-ci show <run-id>
local-ci logs <run-id>
local-ci logs <run-id> --step <step-id>
```

Dirty-worktree publish flow:

```bash
local-ci run
# github posting is skipped because the worktree is dirty
# commit the exact snapshot that just ran
local-ci publish <run-id>
```

Publish only succeeds when the current clean tree and resolved plan still match the stored run.

Explicit local-only flow:

```bash
local-ci run --no-github
# commit the exact snapshot that just ran
local-ci publish <run-id>
```

Inspection notes:
- `show <run-id>` works for finished and active runs from persisted artifacts only
- `show <run-id>` now surfaces the stored snapshot details for the run:
  - `head_tree_hash`
  - `worktree_tree_hash`
  - whether the run executed on a dirty worktree
  - which files were dirty and their blob hashes when known
- `logs <run-id>` defaults to the runner/orchestration view from `events.jsonl`
- `logs <run-id> --step <step-id>` defaults to that step's `combined.log`
- `summary.txt` is the quick index inside each run directory

See `local-ci manual` for the built-in binary-only guide.
See `docs/inspection.md` for the repo copy of the operator guide and the reasoning behind the active-run read path.

Current CLI scope:
- static `[[steps]]` config works
- planner-backed config works
- inspection commands work against `.local-ci/runs/<run-id>/`
- the current checkout must already have a real `HEAD` commit for `run` and `resume`

## Releases and Homebrew

Release tags use the form:

```text
v0.1.0
```

Release builds inject that tag into the binary, so:

```bash
local-ci version
```

prints the installed release version.

The binary also checks GitHub releases and, when a newer tagged release is
available, prints a short update notice at command startup in interactive use:

```text
update available: v0.1.0 -> v0.2.0; run: brew update && brew upgrade local-ci
```

Release automation lives in:
- `.github/workflows/release.yml`
- `scripts/release/write-homebrew-formula.sh`

The Diversio Homebrew tap repo is:
- `https://github.com/DiversioTeam/homebrew-tap`

Once a tagged release exists, install from the tap with:

```bash
brew tap DiversioTeam/tap
brew install local-ci
```

The release workflow will also update the tap formula when the source repo has:

```text
HOMEBREW_TAP_TOKEN
```

configured as a GitHub Actions secret with push access to the tap repo.

## License

MIT — see [`LICENSE`](./LICENSE).

Author/contact:
- Diversio Devs <tech@diversio.com>

## Development

```bash
go test ./...
```

See `docs/architecture.md` and `docs/contracts.md`.

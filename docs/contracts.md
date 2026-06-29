# Contracts

## 1. Config file

Default path: `.local-ci.toml`

Static config example:

```toml
version = 1

[github]
enabled = true
aggregate_context = "local/verify"

[[steps]]
id = "lint"
name = "Lint"
command = ["./scripts/lint.sh"]
dir = "."
needs = []
if = "true"
github_context = "local/lint"
env = { PYTHONUNBUFFERED = "1" }
```

Planner-backed config example:

```toml
version = 1

[github]
enabled = true
aggregate_context = "local/verify"

[planner]
command = ["./scripts/local-ci-plan"]
dir = "."
env = { MODE = "fast" }
```

### Rules

- `version` is required and currently must be `1`.
- `planner.command` is optional.
- `[[steps]]` is required when no planner is configured.
- `[planner]` and `[[steps]]` are mutually exclusive in v1.
- If `planner.command` is set, its stdout JSON is the authoritative resolved plan for that run.
- A resolved planner plan may contain zero steps when there is nothing to run.
- `id` must be unique.
- `command` is always an argv array, not a shell string.
- `dir` is repo-root relative unless absolute.
- `needs` is a small DAG only.
- `github_context` is optional; if missing, the runner defaults to `local/<step-id>`.
- `if` currently supports only `true` and `false`.

## 2. Planner stdout

If a planner is configured, stdout must be valid JSON:

```json
{
  "env": {
    "CHANGED_SCOPE": "python"
  },
  "steps": [
    {
      "id": "unit",
      "command": ["./scripts/unit.sh"],
      "needs": ["lint"],
      "github_context": "local/unit"
    }
  ]
}
```

### Planner rules

- The planner is repo-owned.
- The planner can select steps, omit steps, and inject immutable run-scoped env.
- The planner can also keep a step present but set `if` to `false` when the caller still wants the context emitted as a skipped success.
- The planner should not mutate prior run artifacts.
- The planner should write human-readable debug output to stderr; stdout is reserved for plan JSON.

## 3. Step execution

For each step the engine will:
- create `.local-ci/runs/<run-id>/steps/<nnn-step-id>/`
- set engine-provided env vars
- execute the step command directly without forcing a shell
- capture `stdout.log`, `stderr.log`, and `combined.log`
- persist terminal state in `status.json`
- optionally persist `output.env`

### Engine-provided env vars

#### Common
- `LOCAL_CI_RUN_ID`
- `LOCAL_CI_REPO_ROOT`
- `LOCAL_CI_CONFIG`
- `LOCAL_CI_RUN_DIR`
- `LOCAL_CI_PLAN_FILE`
- `LOCAL_CI_PLAN_ENV`
- `LOCAL_CI_GITHUB_REPO`
- `LOCAL_CI_GITHUB_SHA`

#### Step-specific
- `LOCAL_CI_STEP_ID`
- `LOCAL_CI_STEP_NAME`
- `LOCAL_CI_STEP_INDEX`
- `LOCAL_CI_STEP_DIR`
- `LOCAL_CI_STEP_OUTPUT`

### Step outputs

If a step writes `KEY=VALUE` lines to `LOCAL_CI_STEP_OUTPUT`, the runner persists them to `output.env`. Keys must be valid environment-variable names (`[A-Za-z_][A-Za-z0-9_]*`).

v1 rule: step outputs are for auditability and explicit later consumption, not hidden implicit wiring between steps.

## 4. Run artifacts

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

### `meta.json`

Stores immutable run identity and the trust snapshot for that run:
- run id
- repo root
- repo slug
- HEAD SHA
- config path
- config hash
- plan hash
- created/started/finished timestamps
- `head_tree_hash`
- `worktree_tree_hash`
- `dirty_worktree`
- `dirty_files[]` with path, status, and blob hash when known
- `github_posting_suppressed` when the run intentionally skipped GitHub posting

Why the extra snapshot fields exist:
- `HEAD SHA` answers "which commit was checked out?"
- `worktree_tree_hash` answers "what exact code snapshot actually ran?"
- `dirty_files[]` answers "what was locally different from HEAD?"

### `status.json`

Stores per-step execution facts:
- step id
- terminal state
- started/finished timestamps
- duration
- exit code
- log paths
- GitHub context

Canonical terminal states:
- `success`
- `failure`
- `skipped`
- `blocked`
- `stale`

Transient runtime phases like `pending` and `running` are allowed in memory and events.

### `summary.txt`

`summary.txt` is the human and LLM quick index for a run.

It should make the next move obvious:
- overall state
- artifact directory
- runner log paths
- stored snapshot details (`head_tree_hash`, `worktree_tree_hash`, dirty-worktree state)
- dirty file manifest when the run executed on a dirty worktree
- per-step state overview
- active steps when a run is still in flight
- failure points with exact combined-log paths

Why a plain-text index exists even though `summary.json` also exists:
- `summary.json` is better for machines
- `summary.txt` is faster to scan in a terminal or paste into another tool

### Inspection semantics

`runs` and `show` are read-only inspection commands.
They read persisted artifacts and never attach to the live process.

For an active run, inspection must tolerate short-lived cross-file drift such as:
- `status.json` already updated
- `summary.json` not updated yet

So the inspection contract is:
- try the strict loader first
- retry briefly
- if only the summary is behind, build a best-effort read-only snapshot from persisted step facts

Resume does **not** use that fallback. Resume stays fail-closed.

## 5. Event stream

`events.jsonl` is append-only. Each line is one JSON object.

Core fields:
- `sequence`
- `time`
- `run_id`
- `type`
- `step_id` when relevant
- `status` when relevant
- `message`

Initial event types:
- `run.started`
- `run.finished`
- `step.started`
- `step.finished`
- `step.skipped`
- `step.blocked`
- `step.stale`
- `github.status.posted`
- `github.status.failed`

Reader rule for active runs:
- parse every complete line
- tolerate one partial trailing line from an in-progress append

Why:
- `logs <run-id>` and `show <run-id>` may read while the writer is still appending
- failing on a half-written final line would make active-run inspection brittle for no real gain

## 6. GitHub status posting

v1 uses commit statuses.

### Auth

Preferred runner-specific auth env var:
- `LOCAL_CI_GITHUB_TOKEN`

When invoking `gh api`, the runner will map `LOCAL_CI_GITHUB_TOKEN` to `GH_TOKEN` and `GITHUB_TOKEN` for that subprocess only.

The runner intentionally strips inherited `GH_TOKEN` and `GITHUB_TOKEN` from that subprocess. If `LOCAL_CI_GITHUB_TOKEN` is unset, the runner falls back to the developer's existing `gh auth` session.

Token guidance:
- preferred: fine-grained personal access token with **Commit statuses: Read and write** on the target repo
- acceptable fallback: classic token with `repo:status`

Implementation note:
- v1 posts commit statuses via `gh api POST /repos/{owner}/{repo}/statuses/{sha}`

### Target resolution

Clean-worktree rule:
- when GitHub posting happens during `local-ci run`, the status target is the current `HEAD` commit
- when the worktree is dirty, the runner must not post to GitHub during execution
- a later `local-ci publish <run-id>` may post only if the current clean `HEAD^{tree}` exactly matches the stored run snapshot

The runner must post against:
- the current git repo slug
- the target commit SHA whose tree exactly matches the executed snapshot

Never a parent repo, sibling worktree, cached stale SHA, or a commit whose tree does not match the run snapshot.

### Contexts

- aggregate context defaults to `local/verify`
- per-step context defaults to `local/<step-id>` unless overridden

### Lifecycle

- aggregate: `pending` at run start, terminal at run end
- step: `pending` before execution, terminal on completion
- rerun-from-step must refresh affected step contexts and the aggregate context

## 7. Non-goals

- no pytest/npm/Django built-ins
- no workflow scripting language in TOML
- no DB
- no retry/matrix system
- no second execution path outside the engine

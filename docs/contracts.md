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
- Top-level `max_parallel` is a positive integer, default `1`; it is not a GitHub setting. `run/resume --max-parallel` can override it without changing the selected plan.
- Step `timeout` is an optional positive Go duration such as `10m`; omitted means no deadline. A timeout fails only that step and stops its process group.
- `planner.command` is optional.
- `[[steps]]` is required when no planner is configured.
- `[planner]` and `[[steps]]` are mutually exclusive in v1.
- If `planner.command` is set, its stdout JSON is the authoritative resolved plan for that run.
- A resolved planner plan may contain zero steps when there is nothing to run.
- `id` must be unique.
- `command` is always an argv array, not a shell string.
- `dir` is repo-root relative unless absolute.
- `needs` is a small DAG only. Successful and skipped dependencies satisfy it; failed or blocked dependencies block dependents. Independent branches continue after check failures.
- Steps become ready only after all dependencies are complete, including reused successes. Ties follow the same stable topological order used by serial execution. Completion order may vary.
- Consumer repos must order steps sharing databases, generated files, build directories, or other mutable resources; missing dependency edges are not proof of resource independence.
- `github_context` is optional; if missing, the runner defaults to `local/<step-id>`.
- `if` currently supports only `true` and `false`.

## 2. Planner stdout

If a planner is configured, stdout must be exactly one JSON object. Unknown fields and trailing data are rejected, as in static config, so a misspelled `needs` cannot silently drop an ordering edge:

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
- run the command in its own process group on macOS and Linux
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

### Parallel output and ownership

The scheduler alone writes statuses, summaries, events, and GitHub receipts. Workers own subprocesses and their isolated logs. At `max_parallel > 1`, child output stays in step logs rather than producing interleaved terminal transcripts; progress identifies step IDs and exact failing log paths. At `max_parallel = 1`, raw child output streams live unless `--json` is set.

`run/resume --json` writes one final object with the same schema as `show --json`, including unsuccessful/interrupted results. Child output remains in logs and progress goes to stderr. Usage/preparation/persistence errors may exit without a final JSON object.

`plan [--json]` executes the planner but not verification steps, creates no run artifacts, and posts no statuses. It exposes snapshot/config/plan identity, `max_parallel`, and the complete plan. Planner code may have side effects; this is not guaranteed read-only inspection. Preparation refuses changes to repository identity, HEAD, worktree snapshot, or configuration during planning, before starting checks or creating a run. Generated outputs must use ignored or runner-artifact paths.

`resume --from-step <id>` records stale events and refreshes pending contexts for that step and all dependents, then reruns them while reusing unrelated successes. Other unfinished/failed work is also reconsidered. No partial-plan verification command is provided.

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
- `github_posting_suppressed` when the run skipped GitHub posting or stopped after a post failure
- `max_parallel`: actual worker limit for this execution; absent/zero in legacy metadata means `1`
- `interrupted`: run cancellation, including before any step starts; absent means false

Known suppression reasons are `dirty_worktree`, `cli_disabled`, and `post_failed`.

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
- optional `timeout` copied from the plan and `message` explaining failure, timeout, interruption, skip, or blocking

Canonical terminal states:
- `success`
- `failure`
- `interrupted`
- `skipped`
- `blocked`
- `stale`

Transient runtime phases like `pending` and `running` are allowed in memory and events.

`interrupted` means SIGINT or SIGTERM canceled step execution. New launches stop and every active process group is canceled. The run records
a finish time, leaves work that never started as `pending`, and reruns all
non-successful work on `resume`.

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
- report whether an unfinished run's recorded PID is alive when the platform can determine it

A dead PID is advisory inspection output. Read commands do not rewrite the run
or post a GitHub status, and PID reuse means liveness is not durable run state.

Resume does **not** use that fallback. Resume stays fail-closed.

## 5. Event stream

`events.jsonl` is append-only. Each line is one JSON object. `sequence` defines persistence order. Worker finish timestamps are captured before result collection, so event times need not increase under parallel execution; step durations do not include time waiting for the scheduler to collect a completed result.

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
- `github.status.requested`
- `github.status.posted`
- `github.status.failed`

A `github.status.failed` message records the attempted context and state. The reporter error is returned to the caller, not persisted.

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

- aggregate: `pending` at run start, terminal at run end. The run owns this context even when a step uses the same name; per-step posts cannot overwrite it.
- step: `pending` before execution, terminal on completion
- interrupted step and aggregate contexts post GitHub state `error`
- final interruption posts share one five-second run-wide shutdown budget because the execution context is already canceled
- a shared per-step context cannot pass until all its steps finish; a known failure/error is reported without waiting for siblings and cannot be overwritten by their success. Explicit publication posts each unique context once using the combined result.
- the first remote post failure appends `github.status.failed`, persists `post_failed`, and disables later posts without stopping local execution
- a run suppressed by `post_failed` can use `local-ci publish <run-id>` after auth or connectivity is fixed
- publication leaves original metadata unchanged; explicit retries append new receipts and may duplicate remote statuses
- local artifact or event-write failures remain fatal, including receipt writes during cancellation
- rerun-from-step must refresh affected step contexts and the aggregate context

On the first SIGINT or SIGTERM, the runner cancels the run and gives each active
process group a short grace period. A second signal requests an immediate hard
stop. Final artifact writes remain owned by the engine, not the signal handler.

### Publication evidence, schema 1

The canonical binary-facing field reference is embedded in
[`MANUAL.md`, section 8.1](../cmd/local-ci/MANUAL.md#81-publication-receipts-binary-contract).
Existing `logs --runner --json` exposes the raw events. There is no additional
publication view, pairing engine, coverage calculation, or inferred state.

Typed `github_post` event data contains version, unique attempt ID, repository,
SHA, context, and source (`execution` or `publish`). The shared posting path syncs request intent
before calling the reporter, then syncs acknowledgement/error afterward.
A reporting error is an uncertain outcome, not proof of remote rejection.
Raw API error bodies and credentials are not persisted in receipt fields.

A posted event only establishes acknowledgement when its request ID and exact
fields match a requested event. Consumers must leave malformed, conflicting,
unsupported, or missing evidence unknown; the log reader is not a certifier.

Old/mixed-version or unreadable evidence cannot prove absence of publication.
Recorded targets can have known receipts while additional legacy history stays
unknown. Original run SHA/tree/results are not rewritten by publication.
Receipts are historical acknowledgement, not live status, GitHub commit
existence checks, current-worktree validation, or deployment authorization.

Writers refuse torn event tails rather than silently repairing records.
Use one CLI writer per run; concurrent run/resume/publish commands targeting the same run are unsupported. Parallel step workers inside that writer are supported.
Local artifacts are not signed attestations against manual edits. Explicit
publish retries append records and may duplicate remote statuses; no automatic
retry behavior is added. Existing publish/resume eligibility checks remain.
Posting is explicitly scoped to github.com, matching repository discovery,
regardless of inherited `GH_HOST`.

## 7. Non-goals

- no pytest/npm/Django built-ins
- no workflow scripting language in TOML
- no DB
- no retry/matrix system
- no second execution path outside the engine

# local-ci manual

This is the long-form built-in reference for the `local-ci` binary.

Why this exists:
- developers may only have the binary available
- LLMs need a stable, self-contained teaching surface
- `--help` is good for quick lookup, but not deep learning

---

## 0. How to use this manual

Use this document in three passes:

```text
Pass 1: Sections 1-3   -> understand the tool
Pass 2: Sections 4-7   -> learn the commands and files
Pass 3: Sections 8-11  -> debug real runs
```

Fast command reminder:

```bash
local-ci --help
local-ci help <command>
local-ci manual
```

---

## 1. What this tool is for

`local-ci` runs repo-owned verification steps and stores the results on disk.

First principle:

```text
The runner owns execution.
The repo owns the checks.
The run directory owns the facts.
```

That means:
- `local-ci` does not know Django, pytest, npm, or your repo's workflow details
- your repo provides either static steps or a planner that emits steps
- every run is persisted under `.local-ci/runs/<run-id>/`

---

## 2. Mental model

Think of the tool as two halves:

```text
write path                              read path
------------------------------------    -----------------------------------
local-ci run                            local-ci runs
local-ci resume <run-id>                local-ci show <run-id>
                                         local-ci logs <run-id>
                                         local-ci publish <run-id>
```

Why the split matters:
- the **write path** should be strict, because strictness protects correctness
- the **read path** should be readable, because humans and LLMs need answers fast

This is why active-run inspection works from another shell:
- the reader does not attach to the running process
- the reader just opens the run directory and explains what is already there

---

## 3. Command map

### Start or continue work

```bash
local-ci run
local-ci run --no-github
local-ci resume <run-id>
local-ci resume <run-id> --no-github
```

### Find and inspect work

```bash
local-ci runs
local-ci show <run-id>
local-ci logs <run-id>
local-ci logs <run-id> --step <step-id>
local-ci publish <run-id>
```

### Learn the tool from the binary itself

```bash
local-ci --help
local-ci help <command>
local-ci help all
local-ci manual
```

---

## 4. Run lifecycle

A run moves through a simple lifecycle:

```text
discover repo
-> load config
-> resolve plan
-> create run directory
-> execute steps
-> write logs and status
-> finish with a final summary
```

A run id is the directory name for one execution session.
Example:

```text
20260627T150405Z-deadbeef
```

Why the run id matters:
- it is the handle you use for `resume`, `show`, and `logs`
- it names the artifact directory
- it lets another shell inspect the same run safely

---

## 5. Artifact model

Every run lives here:

```text
.local-ci/runs/<run-id>/
```

Layout:

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

### Why each file exists

#### `meta.json`
Identity and timestamps for the run.

Use it to answer:
- which repo was this?
- which SHA was this?
- when did it start and finish?

#### `plan.json`
The resolved plan that this run actually executed.

Use it to answer:
- what steps existed for this run?
- what dependencies were in effect?

#### `plan.env`
The resolved run-scoped environment emitted by the planner or static config.

#### `summary.json`
Machine-friendly summary of the run.

#### `summary.txt`
Human- and LLM-friendly jump list.

Use it to answer quickly:
- did the run pass?
- did it run on a dirty worktree?
- what exact tree snapshot produced it?
- which step failed?
- which exact log path should I open next?

#### `events.jsonl`
Append-only runner event log.

Use it to answer:
- what happened, in order?

#### `planner.log`
Planner stderr/debug output.

Use it when the plan itself looks wrong.

#### `steps/<nnn-step>/status.json`
Step status facts.

Use it to answer:
- running, success, failure, skipped, blocked, or stale?
- start and finish times?
- log file locations?

#### `stdout.log`, `stderr.log`, `combined.log`
Raw step output.

#### `output.env`
Structured step output file if the step writes `KEY=VALUE` lines.

---

## 6. Log model

There are three different log views because they answer three different questions.

### 6.1 Runner log

Command:

```bash
local-ci logs <run-id>
local-ci logs <run-id> --runner
```

Source:
- `events.jsonl`

Question answered:

```text
What happened in this run?
```

Typical output:
- run started
- step started
- step finished
- step blocked
- run finished

### 6.2 Planner log

Command:

```bash
local-ci logs <run-id> --planner
```

Source:
- `planner.log`

Question answered:

```text
Why did the planner choose this plan?
```

### 6.3 Step logs

Command:

```bash
local-ci logs <run-id> --step <step-id>
local-ci logs <run-id> --step <step-id> --stdout
local-ci logs <run-id> --step <step-id> --stderr
local-ci logs <run-id> --step <step-id> --combined
local-ci logs <run-id> --step <step-id> --output-env
```

Default:
- `--step` without a file selector means `combined.log`

Question answered:

```text
What did this one subprocess print?
```

### Why runner is the default

The first debugging question is usually:

```text
What happened overall?
```

not:

```text
Show me raw stderr for a step I have not identified yet.
```

So runner/orchestration output is the safest default.

---

## 7. Command reference

### 7.1 `local-ci run`

Usage:

```bash
local-ci run [--config <path>] [--no-github]
```

Purpose:
- start a new run
- stream progress live
- persist all artifacts under a new run id

Important behavior:
- child stdout and stderr stream live
- runner progress lines are separate from persisted raw step logs
- on step failure, the CLI prints the exact combined log path
- `--no-github` disables GitHub status posting for that execution

Examples:

```bash
local-ci run
local-ci run --config .local-ci.toml
```

### 7.2 `local-ci resume <run-id>`

Usage:

```bash
local-ci resume <run-id> [--config <path>] [--no-github]
```

Purpose:
- continue a prior run with the same immutable run id

Important behavior:
- resume is strict and fail-closed
- it refuses to continue if repo identity, SHA, config hash, or plan hash changed
- `--no-github` disables GitHub status posting for that execution

Examples:

```bash
local-ci resume 20260627T150405Z-deadbeef
local-ci resume 20260627T150405Z-deadbeef --config .local-ci.toml
```

### 7.3 `local-ci runs`

Usage:

```bash
local-ci runs [--json]
```

Purpose:
- list recent runs from `.local-ci/runs/`

Columns:
- run id
- status
- PID for active runs when known
- started time
- finished time when present
- duration when present

Important behavior:
- newest first
- active runs appear as active, not as malformed finished runs

Examples:

```bash
local-ci runs
local-ci runs --json
```

### 7.4 `local-ci show <run-id>`

Usage:

```bash
local-ci show <run-id> [--json]
```

Purpose:
- give one snapshot view for a finished or active run

This is the main debugging entrypoint.

Important behavior:
- reads persisted artifacts only
- shows the stored runner PID for active runs when known
- shows the stored snapshot details for the run
- does not attach to the process
- works from another shell while the run is still active
- surfaces active steps and failure points first

Examples:

```bash
local-ci show 20260627T150405Z-deadbeef
local-ci show 20260627T150405Z-deadbeef --json
```

### 7.5 `local-ci logs <run-id>`

Usage:

```bash
local-ci logs <run-id> [--runner | --planner | --step <step-id>] [--stdout | --stderr | --combined | --output-env] [--json]
```

Purpose:
- inspect persisted logs without opening files by hand

Selector rules:
- no selector means `--runner`
- `--step` with no file selector means `--combined`
- conflicting selectors are rejected instead of guessed

Examples:

```bash
local-ci logs 20260627T150405Z-deadbeef
local-ci logs 20260627T150405Z-deadbeef --planner
local-ci logs 20260627T150405Z-deadbeef --step checks-fast
local-ci logs 20260627T150405Z-deadbeef --step checks-fast --stderr
```

### 7.6 `local-ci publish <run-id>`

Usage:

```bash
local-ci publish <run-id>
```

Purpose:
- post the stored result of a completed run to the current clean HEAD commit
- avoid rerunning when a dirty-worktree run and a later commit represent the same exact snapshot

Important behavior:
- intended for runs that skipped GitHub posting because the worktree was dirty or `--no-github` was used
- requires a clean current worktree
- requires the current `HEAD^{tree}` to exactly match the stored run snapshot
- requires the current config and resolved plan to still match the stored run
- refuses to republish a run that already posted during execution
- refuses to publish if the code, config, or resolved plan changed after the run

Examples:

```bash
local-ci publish 20260627T150405Z-deadbeef
```

### 7.7 `local-ci manual`

Usage:

```bash
local-ci manual
local-ci help all
```

Purpose:
- print this built-in long-form reference

Use this when:
- you only have the binary
- you want the full mental model, not a quick flag reminder
- an LLM needs to learn the tool without reading the source tree

---

## 8. JSON output model

These commands support `--json`:

```bash
local-ci runs --json
local-ci show <run-id> --json
local-ci logs <run-id> --json
```

First principle:

```text
JSON output should explain the same persisted facts as the plain CLI.
It should not invent a hidden second state model.
```

### `runs --json`

Returns a list of run entries.

Each entry includes:
- `run_id`
- `run_dir`
- `status`
- `started_at`
- `finished_at`
- `duration_millis`
- `error` when a run could not be read normally

### `show --json`

Returns one snapshot object.

It includes:
- top-level run identity
- `meta`
- `summary`
- `steps`
- `runner_log_path`
- `planner_log_path`
- `latest_event` when available

### `logs --json`

Returns the selected log source.

For runner logs:
- `source = "runner"`
- `events = [...]`

For planner or step logs:
- `source`
- `step_id` when relevant
- `view`
- `path`
- `content`

---

## 9. Active-run semantics

Active-run inspection is intentionally read-only and disk-based.

### What can drift during a live run

Files do not all update at the exact same instant.
A real active run can briefly look like this:

```text
status.json  -> already says running
summary.json -> still says pending
events.jsonl -> one more line may still be mid-append
```

That is normal for concurrent file writing.
It is not corruption.

### Why inspection has a best-effort fallback

Strictness is correct for resume.
Readability is correct for inspection.

So inspection follows this rule:

```text
try strict load
-> retry briefly
-> if only summary is behind, rebuild a read-only snapshot from persisted step facts
```

This keeps:
- `resume` strict
- `show` useful
- `runs` useful

### Why the event reader tolerates one partial trailing line

`events.jsonl` is append-only.
That means every complete earlier line is already trustworthy.
The only unstable part is the very last line during an in-progress append.

So the reader does this:

```text
complete line  -> parse it
partial tail   -> ignore it for now
```

That makes active-run inspection stable from another shell.

---

## 10. Debugging playbooks

### I just started a run and want to monitor it

```bash
local-ci runs
local-ci show <run-id>
local-ci logs <run-id>
```

### The run failed and I need the fastest next step

```bash
local-ci show <run-id>
local-ci logs <run-id>
local-ci logs <run-id> --step <failing-step-id>
```

### The planner seems wrong

```bash
local-ci show <run-id>
local-ci logs <run-id> --planner
```

### I only know a step id

```bash
local-ci runs
local-ci show <run-id>
local-ci logs <run-id> --step <step-id>
```

### I want to post a dirty-worktree run after committing the same snapshot

```bash
local-ci run
# github posting skipped because the worktree is dirty
# commit exactly the snapshot that just ran
local-ci publish <run-id>
```

The trust check is intentionally simple:

```text
stored worktree_tree_hash == current HEAD^{tree}
stored config hash        == current config hash
stored plan hash          == current resolved plan hash
```

If any of those differ, the old run is not trusted for posting.

### I want machine-readable output

```bash
local-ci show <run-id> --json
local-ci logs <run-id> --json
```

---

## 11. Safety rules and failure modes

### Resume is intentionally strict

Resume refuses to continue when identity changes.
That is by design.

Why:
- reusing old success across a new SHA or plan is unsafe
- fail-closed is smaller and safer than clever reuse logic

### Inspection is intentionally tolerant

Inspection tries to explain the freshest safe snapshot it can.
That is also by design.

Why:
- showing a useful active-run snapshot is better than failing on a temporary cross-file timing gap
- read-only commands do not mutate state, so this tolerance is low risk

### `run` still requires a real `HEAD`

If a repo has no commit yet, `run` and `resume` fail.
That is expected because the runner uses the current commit identity as part of run safety.


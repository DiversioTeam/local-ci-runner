# Inspection and debugging guide

This document explains the new CLI inspection surface from first principles.

## Why this exists

A local CI runner has two very different jobs:

1. **Run work**
2. **Explain work**

Those jobs sound similar, but they want different behavior.

- The **engine** should be strict, because strictness protects resume safety.
- The **inspection commands** should be readable, because humans and LLMs need fast answers.

That is why the CLI now has a clear read-back surface:

```text
write path                         read path
-------------------------------   -----------------------------------
local-ci run                       local-ci runs
local-ci resume <run-id>           local-ci show <run-id>
local-ci publish <run-id>          local-ci logs <run-id>
```

The write path persists facts.
The read path explains those facts.

## The mental model

A run is just a directory.

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

So the CLI story is:

```text
start a run -> write files -> read files later
```

That simple model is the reason active-run inspection works from another shell.
There is no socket, daemon, or process attachment requirement.

For unfinished runs, inspection performs an advisory liveness check on the
stored runner PID when the platform supports it. A dead PID is displayed as
`(dead)` and exposed as `runner_alive: false` in JSON. Inspection does not turn
that inference into persisted `interrupted` state: SIGKILL, crashes, and power
loss cannot run finalization code, and PIDs can be reused.

## What each command is for

### `local-ci runs`

Use this when you do not remember the run id yet.

It answers:
- what runs exist?
- which one is newest?
- which one is still running?
- which PID owns the active run when known?
- is that recorded PID no longer alive?
- which one failed or was interrupted?

Example:

```bash
local-ci runs
local-ci runs --json
```

### `local-ci show <run-id>`

Use this when you want the fastest high-level answer.

It answers:
- what is the overall state?
- which PID owns the active run when known?
- is that recorded PID no longer alive?
- did this run execute on a dirty worktree?
- what exact tree snapshot produced this run?
- which files were dirty when the run started?
- which step is running now?
- which steps were interrupted, failed, blocked, or went stale?
- which log file should I open next?

Example:

```bash
local-ci show 20260627T150405Z-deadbeef
local-ci show 20260627T150405Z-deadbeef --json
```

### Publication inspection

`logs <run-id> --runner --json` includes typed publication events with exact
targets and timestamps. There is no separate publication summary. These records
are historical facts, not proof the latest resumed result was fully published.
Missing or malformed evidence remains unknown. Execution-time settings are not receipts.

This is offline inspection, not proof that GitHub statuses are still current,
that a commit has been pushed, or that the current worktree/plan is validated.
See `local-ci manual` section 8.1 for the complete version-matched JSON contract.

### `local-ci publish <run-id>`

**Write command, not a dry run:** may execute the repo planner and posts
GitHub statuses. Obtain separate authorization; do not use it as a trust probe.

Use this when a run completed on a dirty worktree and intentionally skipped
GitHub posting, but you later committed the exact same snapshot.

It answers:
- can I trust this old run for the current clean HEAD?
- if yes, can I post it without rerunning?

Example:

```bash
local-ci publish 20260627T150405Z-deadbeef
```

### `local-ci logs <run-id>`

Use this when you already know which level of detail you want.

It answers three different questions:

```text
What happened overall?      -> --runner (default)
What did the planner say?   -> --planner
What did one step print?    -> --step <step-id>
```

Examples:

```bash
local-ci logs 20260627T150405Z-deadbeef
local-ci logs 20260627T150405Z-deadbeef --planner
local-ci logs 20260627T150405Z-deadbeef --step checks-fast
local-ci logs 20260627T150405Z-deadbeef --step checks-fast --stderr
```

## Why runner logs are the default

When a run fails, the first question is usually not:

> show me raw stderr for some step I have not identified yet

The first question is:

> what happened in this run?

That is why `local-ci logs <run-id>` defaults to the runner/orchestration view from `events.jsonl`.

It gives a short timeline first:
- run started
- step started
- step finished
- step blocked
- run finished

Then, after `show` or the runner log points at the interesting step, you can open raw step logs.

## Why `summary.txt` exists

`summary.json` is for machines.
`summary.txt` is for quick reading.

The goal of `summary.txt` is not to be clever. It is to be a jump list.

It should answer:
- what run is this?
- did it pass?
- did it run on a dirty worktree?
- what tree snapshot produced it?
- where are the runner logs?
- which step failed or was interrupted?
- which exact combined log path should I open next?

That plain-text index helps both humans and LLMs.

## Active runs: why inspection needs a best-effort fallback

A still-running run updates files over time.

Very roughly, a step transition looks like this:

```text
1. write step status.json
2. write summary.json
3. append events.jsonl
```

That means a reader can briefly catch the run between writes.
For example:
- the step status already says `running`
- the summary still says `pending`

That is not corruption. It is just an in-progress snapshot.

### Strict load vs inspection load

We keep two behaviors on purpose:

```text
resume path   -> strict, fail closed
inspect path  -> best effort, read only
```

Why:
- `resume` must reject drift, because it can change execution behavior
- `show` and `runs` should still explain the run, because they are read-only

So inspection now does this:

```text
try strict load
-> retry briefly
-> if summary is still behind, rebuild a read-only summary from step statuses
```

This keeps the persisted files canonical while still making active runs understandable.

## Why `events.jsonl` readers ignore one partial trailing line

`events.jsonl` is append-only.
That is good for concurrent reading.

But there is one trap: the last line may be half-written when another shell reads it.

So the reader follows a simple rule:

```text
complete line  -> parse it
partial tail   -> ignore it for now
```

Why this is safe:
- every earlier line is already complete
- the next read will see the finished line
- inspection stays stable instead of failing on a temporary half-line

## Suggested debugging flows

### A run is still in progress

```bash
local-ci runs
local-ci show <run-id>
local-ci logs <run-id>
local-ci logs <run-id> --step <running-step-id>
```

### A run failed or was interrupted

```bash
local-ci show <run-id>
local-ci logs <run-id>
local-ci logs <run-id> --step <failing-or-interrupted-step-id>
local-ci logs <run-id> --step <failing-or-interrupted-step-id> --stderr
local-ci resume <run-id> # when the stored identity still matches
```

### I need to know whether an old local run is still trustworthy

```bash
local-ci show <run-id>
# inspect head_tree_hash, worktree_tree_hash, dirty_worktree, dirty_files
local-ci logs <run-id> --runner --json
# Inspect exact publication events. This does not validate the current checkout.
```

Only after explicit authorization, `publish` rechecks its existing eligibility
conditions and may execute the planner. Refusal can also mean GitHub was
disabled, the run is unfinished/interrupted, or posting was not suppressed.
It is not necessarily a snapshot mismatch. After posting errors, inspect
receipts before considering an explicit retry; success and local persistence
cannot be atomic with GitHub.

### A planner-backed run looks wrong

```bash
local-ci show <run-id>
local-ci logs <run-id> --planner
```


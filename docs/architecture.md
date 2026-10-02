# Architecture

## Rule

The runner understands processes and files. Consumer repos own the actual verification logic.

## Layers

- `cmd/local-ci`: CLI entrypoint
- `internal/config`: config loader + validation
- `internal/planner`: optional planner contract
- `internal/engine`: run orchestration and state transitions
- `internal/persistence`: on-disk layout and filenames
- `internal/events`: append-only event schema
- `internal/github`: GitHub commit status reporting

## Data flow

### Write path

1. Discover repo root, repo slug, HEAD SHA, and worktree snapshot.
2. Load `.local-ci.toml`.
3. Get the static `[[steps]]` plan or execute the planner for stdout JSON. Refuse input changes during planning.
4. Create the run directory and `meta.json` for those confirmed inputs.
5. Persist `plan.json` and `plan.env`.
6. Execute ready steps under one run context, bounded by `max_parallel` (default 1).
7. On SIGINT/SIGTERM, stop launching new work and cancel every active process group on macOS and Linux.
8. Persist per-step status and logs.
9. Append lifecycle events to `events.jsonl`.
10. Sync typed intent, post step/aggregate GitHub statuses, then sync outcome receipts.
    After the first remote failure, persist `post_failed` and continue locally without further posts.
11. Write `summary.json` and `summary.txt`.

### Read path

The read-back commands do not attach to the running process.
They read the persisted run directory instead.

```text
local-ci runs                -> list run directories
local-ci show <run-id>       -> snapshot view over persisted state
local-ci logs <run-id>       -> render runner, planner, or step logs
```

This split is intentional. `publish` is a write command, not an inspection
probe: it can execute the planner and writes GitHub statuses and local receipts.

Publication extends the existing event stream with request IDs and exact
targets. `logs --runner --json` exposes those records unchanged. No separate
publication model, parser, state machine, or live-status service is introduced.

Signal handling follows the same ownership rule:
- the CLI translates process-wide SIGINT/SIGTERM into run-context cancellation
- the engine owns process shutdown and all final persisted writes
- a signal goroutine never writes run artifacts

Why:
- the engine can stay strict about persisted state and resume safety
- the CLI can stay simple and readable for humans and LLMs
- active-run inspection works from another shell because the files are the contract

### Active-run inspection rule

During a running step, files do not all update at the exact same moment.
For example, `status.json` may advance before `summary.json` catches up.

So inspection follows this rule:
- strict load first
- short retry next
- best-effort read-only snapshot last

That fallback is only for inspection.
Resume stays fail-closed.

## Persistence

Run state is file-based on purpose:
- easy to inspect
- easy to diff
- easy to recover
- easy for humans and LLMs
- no service or DB to babysit

See `docs/inspection.md` for the operator-facing explanation of why the read path works this way and how to debug a run quickly.

## Resume safety

Resume must fail closed if any of these changed:
- repo identity
- HEAD SHA
- config hash
- planner output hash

The runner can reuse prior successful steps only inside the same immutable run identity.
Interrupted and otherwise unfinished steps are rerun.

## Parallel execution

`internal/engine/schedule.go` launches ready steps up to `max_parallel`. Workers receive immutable identity/plan data and return typed results; only the scheduler mutates statuses, summaries, events, and GitHub state. Step failures do not cancel independent siblings. Successful and skipped dependencies satisfy `needs`.

Every worker derives from the run context, owns one process group and isolated logs, and can have an optional step timeout. A timeout fails that step; run cancellation interrupts active workers and leaves unstarted work pending. Fatal local persistence errors cancel and join all active workers before returning.

Shared GitHub contexts combine all matching step results, including during publication. Interruption reporting shares one bounded shutdown budget. Execution and inspection use the same engine summary builder.

The concurrency limit controls commands, not their internal worker pools. Consumer repos own database/cache/output isolation and toolchain preparation. See [the implementation and rollout checklist](plans/parallel-execution.md).


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

1. Load `.local-ci.toml`.
2. Resolve repo root, repo slug, and HEAD SHA.
3. Create run directory and `meta.json`.
4. Resolve a plan:
   - static `[[steps]]`, or
   - planner stdout JSON
5. Persist `plan.json` and `plan.env`.
6. Execute steps in dependency order under one run context.
7. On SIGINT/SIGTERM, cancel that context and stop the active step, including its process group on macOS and Linux.
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

## Future parallel execution

The run context is a broadcast boundary. Future independent step goroutines will
all derive their step contexts from it, so one cancellation reaches every active
worker. Each worker owns one process group and isolated log files. The scheduler
remains the single writer for statuses, summaries, events, and aggregate GitHub
state; step failures are results and must not cancel independent siblings.


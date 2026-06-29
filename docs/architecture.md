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
6. Execute steps in dependency order.
7. Persist per-step status and logs.
8. Append lifecycle events to `events.jsonl`.
9. Post step and aggregate GitHub statuses.
10. Write `summary.json` and `summary.txt`.

### Read path

The read-back commands do not attach to the running process.
They read the persisted run directory instead.

```text
local-ci runs                -> list run directories
local-ci show <run-id>       -> snapshot view over persisted state
local-ci logs <run-id>       -> render runner, planner, or step logs
```

This split is intentional.

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


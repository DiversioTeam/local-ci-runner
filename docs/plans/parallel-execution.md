# Parallel execution: implementation and rollout

## Baseline

Reviewed checkout: `52744b9` (before these source changes). The original Downloads build plan and July improvement notes are historical, not an outstanding-work checklist.

Already present at this baseline:
- SIGINT/SIGTERM cancellation, per-step process groups, interrupted state, and read-only dead-PID inspection
- posting-failure suppression that continues local checks and allows later explicit publication
- typed publication intents/outcomes in the existing event log
- fail-closed snapshot/config/plan checks for resume/publication

Publication leaves the original run metadata unchanged. Receipts are historical acknowledgements, not live CI status or validation evidence. A remote failure remains an uncertain remote outcome.

## Implemented in source

- [x] Positive top-level `max_parallel`, default 1, with CLI override and recorded execution limit
- [x] Ready-step scheduling, bounded command workers, one shared-state writer
- [x] Continue independent branches after ordinary failures; success/skipped satisfy dependencies
- [x] Isolated raw logs, worker finish times without result-collection delays, explicit progress, and truthful shared GitHub contexts
- [x] Positive optional per-step timeout using existing process-group cancellation
- [x] Stop new launches, cancel/join active workers, and finalize interrupted runs, including before any worker starts
- [x] One run-wide shutdown-reporting budget; local persistence failures remain fatal
- [x] `plan --json`, `run/resume --json`, and `resume --from-step`
- [x] Version-matched help/manual, field documentation, and binary-only discovery tests
- [x] Runner tests with race detection, event-sequence concurrency assertions, force-stop broadcasts, fatal-persistence worker cleanup, and CLI JSON checks
- [x] Consumer DAG/config changes and operator instructions for backend, frontend, Optimo frontend, design-system, and infrastructure
- [x] Cross-repo planner and database/cache-isolation checks without credentials or installations

## Fresh-eyes review fixes

- Keep receipt-write errors fatal even when pending reporting is canceled.
- Let only the run post its aggregate context, including when a step uses the same name.
- Refuse planner changes to repository identity, HEAD, tracked/untracked source snapshot, or configuration before creating a run or executing checks; ignored outputs remain allowed.
- Treat log open/write/close failures as fatal local persistence errors, cancel their command, and join sibling workers; preserve log errors even when `Cmd.Run` reports a different exit error.
- Print failure/interruption log paths before remote reporting can delay them.
- Honor frontend `PNPM_HOME` in setup and every package lane; document re-preparing setup after another repo changes the shared toolchain.
- Run backend pytest without replacing its wrapper shell, so the EXIT trap removes temporary Testmon copies after success or failure without changing the pytest exit code.
- Record a timeout only for a command that failed; a command that exited cleanly stays successful even if its deadline passed while results were read.
- Reject unknown planner fields and trailing output, as static config does. With `max_parallel > 1`, a misspelled `needs` would otherwise drop an ordering edge between steps that share resources.

Each behavior has regression coverage. Backend local-workspace review uses fetched `origin/dev`, HEAD, merge base, and policy root `dec3997326848b2d712dd6cce127e512a6dffe9e`; staged/unstaged/untracked workspace changes are included. Tests are isolated fixtures/mocked commands, not full application suites or publication evidence.

## Consumer safety rules

| Repo | Limit | Rules |
| --- | --- | --- |
| backend | 3 | Fast/SAST/deep lanes independent. Product tests wait for deep checks; hash the worktree/run/product into short distinct DB suffixes and use step-local pytest caches. |
| frontend | 3 | Prepare Node/pnpm once. Package checks depend on setup, not one another. Single-worker auth tests and step-local Jest caches/coverage. |
| Optimo frontend | 3 | Independent checks; test pools capped at two workers each, separate Vite caches and coverage paths. Combine shared required GitHub contexts. |
| design-system | 3 | Prepare toolchain once. Build/audit independent; tests wait for build, then run with single-worker Jest and isolated caches/coverage. |
| infrastructure | 2 | Checkov and locked audit independent; no Terraform/Terragrunt plan/apply, scanner scope or advisory-policy change. |

Command limits do not limit internal worker pools. Resource independence belongs to consumer repos: retain dependency edges when commands share mutable outputs. Different repositories must not switch a shared global Node toolchain concurrently.

## Verification

From the runner checkout:

```bash
gofmt -w cmd internal
go test ./...
go test -race ./...
go test -race ./internal/engine -run 'TestParallelRun|TestCanceledRun|TestRerunFromStep' -count=100
go vet ./...
go build -o /tmp/local-ci-parallel ./cmd/local-ci
/tmp/local-ci-parallel run --no-github --json
/tmp/local-ci-parallel help plan
/tmp/local-ci-parallel help run
/tmp/local-ci-parallel help resume
/tmp/local-ci-parallel manual
```

From the monolith checkout:

```bash
uv run --no-project --python 3.14 python -m unittest scripts.tests.test_local_ci_parallel_plans
```

The consumer test runs actual JSON-emitter code with controlled routing inputs and mocked backend commands. It does not run full consumer suites, install dependencies, load secrets, fetch branch refs, or publish statuses. It is not evidence of real application-check performance.

## Release and rollout still required

- [ ] Release the runner before merging consumer configs that older binaries reject.
- [ ] Install dependencies/toolchains, then run a real local-only validation in each consumer repo.
- [ ] Compare serial and parallel wall-clock times on representative changes; do not claim a measured speedup from scheduler unit tests.
- [ ] Check memory/DB pressure before increasing these limits or nested test workers.

For a serial fallback, use `run/resume --max-parallel 1`; do not edit the plan to skip checks. Config/plan changes invalidate old runs as before. One CLI writer per run remains required; no external concurrent resume/publish support or automatic migration is added.

## Deferred

No retention command, follow/watch UI, extra doctor implementation, resource-lock framework, database/daemon, or Rust rewrite. Add these only for a demonstrated need. Snapshot ARG_MAX handling and unrelated update/transport cleanups remain separate work.

# Adding and changing tests

Every test here must protect a behavior someone relies on, and must fail when
that behavior breaks. A test that cannot fail, or that fails only when code is
rearranged, costs review time and teaches nothing. This guide is the bar for
new tests and for changes to existing ones.

## Before you write a test

Answer four questions. If you cannot answer one, do not add the test yet.

1. **What contract does it protect?** Name the behavior, invariant, or
   documented field: `docs/contracts.md`, `MANUAL.md`, or a non-negotiable rule
   in `AGENTS.md`.
2. **What credible regression makes it fail?** Say which line someone could
   plausibly change.
3. **Why doesn't existing coverage catch that?** Each contract has one owning
   test at the strongest boundary (see the map below). Prefer a new row in the
   owner's table over a near-duplicate test.
4. **Does it need a production seam no production caller uses?** If yes, test
   the real boundary instead. Do not add exported fields, function variables,
   or option hooks only for tests.

A bug fix's regression test must fail on the code before the fix, for the
reason the bug describes. Run it against the old code once and see it fail.

## Where a contract is owned

Test a contract once, at the layer that owns it. Other layers may test
something different, such as wiring, but not repeat the same contract.

| Contract | Owner | Boundary to test through |
| --- | --- | --- |
| `.local-ci.toml` rules: version, ids, needs, cycles, `if`, `timeout`, `max_parallel`, unknown keys | `internal/config` | `Load` with TOML text: one row per rule in `TestLoadRejectsInvalidConfig` |
| Planner process: strict JSON stdout, stderr log, `LOCAL_CI_*` env, identity wins over `planner.env` | `internal/planner` | `Execute` with a script run through `/bin/sh` |
| On-disk layout and env-file codec | `internal/persistence` | Literal expected paths and bytes, never values built from the constants under test |
| Event log: sequence continuity, torn-tail refusal, malformed lines | `internal/events` | Real files, with a second appender for continuity |
| Scheduling, cancellation, timeouts, shared GitHub contexts, resume and rerun | `internal/engine` | `ExecuteRun` / `PublishCompletedRun` with `runFixture` |
| Run-state publish eligibility: finished, not interrupted, posting suppressed | `internal/engine` | `PublishCompletedRun` |
| Publish identity and snapshot checks; planner-change refusal | `cmd/local-ci` | `validatePublishableRun` table; `plan`/`run` through the CLI |
| Flags, usage errors, exit status, stdout/stderr separation, JSON purity | `cmd/local-ci` | `runWithContext`; the compiled binary for exit codes and help/manual |
| `gh` argv and env | `internal/github` | A fake `gh` on `PATH` |
| Repo identity and worktree snapshot | `internal/gitrepo` | Real `git` repositories, no mocks |
| Self-update and Homebrew handoff | `internal/update` | A local `httptest` server, a real symlink into a `Cellar`, a fake `brew` on `PATH` |

## Patterns that keep tests deterministic

**Concurrency comes from the event log, not from timing.** The scheduler
appends `step.started` before it launches a worker and `step.finished` after it
collects the result. Replay `events.jsonl` to get the peak number of active
steps and the dependency order (see
`TestParallelRunLimitsWorkersAndWaitsForDependencies`). Never assert that a
marker file does *not* exist yet; that check races with the worker.

**Use markers only as barriers.** Steps can wait for a marker the test writes
(`while [ ! -f release ]; do sleep 0.02; done`), and the test can wait for a
marker a step writes (`validateMarkerExists`). Prefer shell builtins
(`: > started`) and `exec`, so no child process holds the step's pipes during
cancellation.

**Prove cancel *and* join.** If a test claims "workers are stopped before
returning", give the sibling a `TERM` trap that writes a marker after a short
delay. Then assert the marker exists the moment `ExecuteRun` returns. A plain
"returned within 5s" bound also passes when workers are orphaned.

**Publish only runs that were prepared to be published.** Use
`prepareSuppressedRunFixture` (engine) or `run --no-github` (CLI). Never set
`GitHubPostingSuppressed` or `GitHubEnabled` on a result in memory: real
publication loads the run from disk, so an in-memory edit tests a state that
cannot happen.

**Negatives must hit the guard they name.** Start from a valid input, change
exactly one field, and assert that guard's own message. A bare `err != nil`
passes when an earlier, unrelated guard fires. Include a positive row
(`unchanged`) so the table proves the base input is accepted.

**Fakes record; they don't decide.** `fakeReporter` records posts and can be
told to fail. It must never compute the result the test asserts. It has no
lock because only the scheduler goroutine reports, and `-race` enforces that.

**Use real boundaries instead of seams.** When behavior depends on the
environment, change the environment, not the code. Use `t.Setenv("PATH", dir)`
with a fake `gh` or `brew`, a real symlink, or a local HTTP server. Such tests
cannot use `t.Parallel()`. Two past release bugs were hidden by injected seams
that replaced the code that broke: an HTTP client with the wrong timeout, and a
command runner that skipped the real `exec` path.

**Pin user-facing bytes, not prose.** Assert flag names, `Usage:` lines, event
types, schema versions, and exact manual output, which operators and LLMs
depend on. Don't assert on explanatory sentences someone may reword. Persisted
field names are checked by reflection in
`TestBuiltBinaryDocumentsReceiptsWithoutSourceOrGit`.

**Isolate host configuration.** Packages that run `git` set
`GIT_CONFIG_GLOBAL=/dev/null` and `GIT_CONFIG_NOSYSTEM=1` in `TestMain`, so a
developer's signing, hooks, or default branch cannot change results. Run
scripts through `/bin/sh path`, not by executing a file you just wrote: that
can fail with `ETXTBSY` on Linux while parallel tests fork.

## Prove the test can fail

Before you land a new or repaired test, break the production line it protects
and watch it fail:

```bash
rsync -a --exclude .git --exclude .local-ci ./ /tmp/local-ci-mutant/
# edit one production line in /tmp/local-ci-mutant
(cd /tmp/local-ci-mutant && go test -count=1 ./internal/engine -run TestYourTest)
```

The test must fail with *its own* message, not a build error or another test's
assertion. If it stays green, it does not protect what its name claims. Fix it
before landing it. Mutate a copy, never your checkout.

## Platform coverage

The log write-failure row in `TestParallelRunLogFailureIsFatalLocalError` needs
`/dev/full`, so it runs on Linux and skips on macOS. Release CI runs on Linux.
To run the full suite on Linux locally, with no network and no host
environment:

```bash
rsync -a --delete --exclude .git --exclude .local-ci ./ /tmp/local-ci-linux/
(cd /tmp/local-ci-linux && go mod vendor)
docker run --rm --network none --user 1000:1000 \
  -e HOME=/tmp -e GOFLAGS=-mod=vendor -e GOCACHE=/tmp/gocache -e GOTOOLCHAIN=local \
  -v /tmp/local-ci-linux:/src -w /src golang:1.27.1 go test -race ./...
```

Use the Go version from `go.mod` or newer. Running as a non-root user matters:
root bypasses the permission failures some tests rely on.

## Checklist

- [ ] Contract named, regression named, owner chosen from the map
- [ ] Added a row to the owner's table instead of a near-duplicate test
- [ ] No new production field or hook exists only for tests
- [ ] Negatives start from a valid input and assert their own guard's message
- [ ] No sleeps that wait for something to *not* happen; no in-memory edits of persisted state
- [ ] Mutated the production line and saw this test fail for the right reason
- [ ] `gofmt`, `go vet ./...`, `go test ./...`, `go test -race ./...` pass

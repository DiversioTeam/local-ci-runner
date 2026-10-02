package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DiversioTeam/local-ci-runner/internal/config"
	"github.com/DiversioTeam/local-ci-runner/internal/events"
	ghstatus "github.com/DiversioTeam/local-ci-runner/internal/github"
	"github.com/DiversioTeam/local-ci-runner/internal/persistence"
)

func TestParallelRunFinishTimesExcludeResultCollectionDelay(t *testing.T) {
	t.Parallel()
	plan := config.ResolvedPlan{Steps: []config.Step{
		{ID: "first", Command: []string{"sh", "-c", "while [ ! -f second-started ]; do sleep 0.02; done"}},
		{ID: "second", Command: []string{"sh", "-c", "touch second-started; while [ ! -f reporting-started ]; do sleep 0.02; done"}},
	}}
	plan.ApplyDefaults()
	fixture := newRunFixtureWithGitHub(t, plan, config.GitHub{Enabled: true})
	run := prepareRunFixture(t, fixture)
	run.Meta.MaxParallel = 2
	var clockDelay atomic.Int64
	var captureNextFinish atomic.Bool
	finishCaptured := make(chan struct{})
	now := func() time.Time {
		at := run.Meta.CreatedAt.Add(time.Duration(clockDelay.Load()))
		if captureNextFinish.CompareAndSwap(true, false) {
			close(finishCaptured)
		}
		return at
	}
	reporter := publicationReporter(func(_ ghstatus.Target, status ghstatus.Status) error {
		if status.Context != "local/first" || status.State != ghstatus.StateSuccess {
			return nil
		}
		captureNextFinish.Store(true)
		writeFile(t, filepath.Join(fixture.repoRoot, "reporting-started"), nil)
		select {
		case <-finishCaptured:
			clockDelay.Store(int64(time.Hour))
		case <-time.After(5 * time.Second):
			t.Fatal("worker did not capture its finish time while the scheduler was reporting")
		}
		return nil
	})
	result, err := ExecuteRun(t.Context(), fixture.store, run, ExecuteOptions{Now: now, Reporter: reporter})
	if err != nil {
		t.Fatal(err)
	}
	if result.StepStatuses[1].DurationMillis != 0 || !result.StepStatuses[1].FinishedAt.Equal(run.Meta.CreatedAt) {
		t.Fatalf("second step includes result-collection delay: %+v", result.StepStatuses[1])
	}
}

func TestParallelRunLimitsWorkersAndWaitsForDependencies(t *testing.T) {
	t.Parallel()
	for _, maxParallel := range []int{2, 4} {
		t.Run(fmt.Sprintf("max_parallel=%d", maxParallel), func(t *testing.T) {
			t.Parallel()
			plan := config.ResolvedPlan{Steps: []config.Step{
				{ID: "after", Needs: []string{"first", "second"}, Command: []string{"sh", "-c", "printf after"}},
				{ID: "first", Command: []string{"sh", "-c", "printf first"}},
				{ID: "second", Command: []string{"sh", "-c", "printf second"}},
				{ID: "third", Command: []string{"sh", "-c", "printf third"}},
			}}
			plan.ApplyDefaults()
			fixture := newRunFixture(t, plan)
			run := prepareRunFixture(t, fixture)
			run.Meta.MaxParallel = maxParallel
			result, err := ExecuteRun(t.Context(), fixture.store, run, ExecuteOptions{})
			if err != nil || result.Summary.Status != string(StepStateSuccess) {
				t.Fatalf("summary = %s, error = %v", result.Summary.Status, err)
			}

			// The scheduler appends step.started before launching and step.finished after
			// collecting a result, so the event log replays exactly how many steps were active.
			startedAt, finishedAt := make(map[string]int64), make(map[string]int64)
			activeSteps, peakActiveSteps := 0, 0
			for index, event := range mustReadEvents(t, fixture.store.RunFile(run.RunID, persistence.EventsFile)) {
				if event.Sequence != int64(index+1) {
					t.Fatalf("event sequence = %d at index %d", event.Sequence, index)
				}
				switch event.Type {
				case events.StepStarted:
					if _, started := startedAt[event.StepID]; started {
						t.Fatalf("%s started twice", event.StepID)
					}
					startedAt[event.StepID] = event.Sequence
					activeSteps++
					peakActiveSteps = max(peakActiveSteps, activeSteps)
				case events.StepFinished:
					finishedAt[event.StepID] = event.Sequence
					activeSteps--
				}
			}
			// Three steps are ready at once, so a larger limit must not serialize them either.
			if want := min(maxParallel, 3); peakActiveSteps != want {
				t.Fatalf("peak active steps = %d, want %d", peakActiveSteps, want)
			}
			for _, need := range []string{"first", "second"} {
				if startedAt["after"] < finishedAt[need] {
					t.Fatalf("after started at event %d before %s finished at event %d", startedAt["after"], need, finishedAt[need])
				}
			}
			for index, step := range plan.Steps {
				if log := mustReadFile(t, fixture.store.StepFile(run.RunID, index, step.ID, persistence.StdoutLog)); log != step.ID {
					t.Fatalf("%s stdout log = %q", step.ID, log)
				}
			}
			loaded, err := LoadRun(fixture.store, run.RunID)
			if err != nil || loaded.Meta.MaxParallel != maxParallel {
				t.Fatalf("persisted max_parallel = %d, error = %v", loaded.Meta.MaxParallel, err)
			}
		})
	}
}

func TestParallelRunKeepsChildOutputInStepLogs(t *testing.T) {
	t.Parallel()
	plan := config.ResolvedPlan{Steps: []config.Step{
		{ID: "first", Command: []string{"sh", "-c", "printf out-first; printf err-first >&2"}},
		{ID: "second", Command: []string{"sh", "-c", "printf out-second; printf err-second >&2"}},
	}}
	plan.ApplyDefaults()
	fixture := newRunFixture(t, plan)
	run := prepareRunFixture(t, fixture)
	run.Meta.MaxParallel = 2
	var stdout, stderr bytes.Buffer
	if _, err := ExecuteRun(t.Context(), fixture.store, run, ExecuteOptions{Stdout: &stdout, Stderr: &stderr}); err != nil {
		t.Fatal(err)
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("parallel child output reached the terminal: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	for index, step := range plan.Steps {
		for logName, want := range map[string]string{persistence.StdoutLog: "out-" + step.ID, persistence.StderrLog: "err-" + step.ID} {
			if got := mustReadFile(t, fixture.store.StepFile(run.RunID, index, step.ID, logName)); got != want {
				t.Fatalf("%s %s = %q, want %q", step.ID, logName, got, want)
			}
		}
	}
}

func TestParallelRunFailureDoesNotCancelSiblingsOrOverwriteSharedContext(t *testing.T) {
	t.Parallel()
	plan := config.ResolvedPlan{Steps: []config.Step{
		{ID: "fail", GitHubContext: "shared", Command: []string{"sh", "-c", "exit 3"}},
		{ID: "sibling", GitHubContext: "shared", Command: []string{"sh", "-c", "touch sibling-started; while [ ! -f release ]; do sleep 0.02; done"}},
		{ID: "blocked", Needs: []string{"fail"}, Command: []string{"sh", "-c", "exit 99"}},
		{ID: "skipped", If: "false", Command: []string{"sh", "-c", "exit 99"}},
		{ID: "after-skip", Needs: []string{"skipped"}, Command: []string{"sh", "-c", "exit 0"}},
	}}
	plan.ApplyDefaults()
	fixture := newRunFixtureWithGitHub(t, plan, config.GitHub{Enabled: true})
	run := prepareRunFixture(t, fixture)
	run.Meta.MaxParallel = 2
	reporter := &fakeReporter{}
	results, _ := getAsyncRun(t, fixture, run, ExecuteOptions{Reporter: reporter})
	validateMarkerExists(t, filepath.Join(fixture.repoRoot, "sibling-started"))
	writeFile(t, filepath.Join(fixture.repoRoot, "release"), nil)
	result := getFinishedRun(t, results)
	states := statusStates(result.StepStatuses)
	if states["fail"] != "failure" || states["sibling"] != "success" || states["blocked"] != "blocked" || states["after-skip"] != "success" {
		t.Fatalf("states = %v", states)
	}
	validateSharedContextNeverPasses(t, reporter)

	// Explicit publication posts each context once, from the combined result of its steps.
	publishFixture := newRunFixtureWithGitHub(t, plan, config.GitHub{Enabled: true})
	writeFile(t, filepath.Join(publishFixture.repoRoot, "release"), nil)
	suppressed := executeSuppressedRun(t, publishFixture)
	publishReporter := &fakeReporter{}
	if err := PublishCompletedRun(t.Context(), publishFixture.store, suppressed, PublishOptions{Reporter: publishReporter, TargetSHA: "def456"}); err != nil {
		t.Fatal(err)
	}
	validateSharedContextNeverPasses(t, publishReporter)
	sharedPosts := 0
	for _, post := range publishReporter.posts {
		if post.status.Context == "shared" {
			sharedPosts++
		}
	}
	if sharedPosts != 1 {
		t.Fatalf("publication posted the shared context %d times, want once", sharedPosts)
	}
}

func TestStepContextCannotPublishAggregateSuccessBeforeRunFinishes(t *testing.T) {
	t.Parallel()
	// The "verify" step's default context collides with the run's aggregate context.
	plan := config.ResolvedPlan{Steps: []config.Step{
		{ID: "verify", Command: []string{"sh", "-c", "exit 0"}},
		{ID: "other", Command: []string{"sh", "-c", "exit 0"}},
	}}
	plan.ApplyDefaults()
	fixture := newRunFixtureWithGitHub(t, plan, config.GitHub{Enabled: true})
	run := prepareRunFixture(t, fixture)
	run.Meta.MaxParallel = 2
	aggregateSuccesses := 0
	reporter := publicationReporter(func(_ ghstatus.Target, status ghstatus.Status) error {
		if status.Context != run.Meta.GitHubAggregateContext || status.State != ghstatus.StateSuccess {
			return nil
		}
		aggregateSuccesses++
		storedMeta, err := persistence.ReadJSONFile[persistence.Meta](fixture.store.RunFile(run.RunID, persistence.MetaFile))
		if err != nil {
			t.Fatal(err)
		}
		if storedMeta.FinishedAt == nil {
			t.Error("step posted aggregate success before run finalization")
		}
		return nil
	})
	if _, err := ExecuteRun(t.Context(), fixture.store, run, ExecuteOptions{Reporter: reporter}); err != nil || aggregateSuccesses != 1 {
		t.Fatalf("aggregate successes = %d, execution error = %v", aggregateSuccesses, err)
	}

	publishFixture := newRunFixtureWithGitHub(t, plan, config.GitHub{Enabled: true})
	suppressed := executeSuppressedRun(t, publishFixture)
	if err := PublishCompletedRun(t.Context(), publishFixture.store, suppressed, PublishOptions{Reporter: reporter, TargetSHA: "def456"}); err != nil {
		t.Fatal(err)
	}
	if aggregateSuccesses != 2 {
		t.Fatalf("publication posted the aggregate context %d times, want once", aggregateSuccesses-1)
	}
}

func TestPendingReportingPreservesReceiptErrorsDuringCancellation(t *testing.T) {
	t.Parallel()
	for _, stepPending := range []bool{false, true} {
		t.Run(map[bool]string{false: "aggregate", true: "step"}[stepPending], func(t *testing.T) {
			plan := config.ResolvedPlan{Steps: []config.Step{{ID: "test", Command: []string{"true"}}}}
			plan.ApplyDefaults()
			fixture := newRunFixtureWithGitHub(t, plan, config.GitHub{Enabled: true})
			run := prepareRunFixture(t, fixture)
			eventsPath := fixture.store.RunFile(run.RunID, persistence.EventsFile)
			appender, err := events.NewAppender(eventsPath, run.RunID)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancelRun := context.WithCancel(t.Context())
			defer cancelRun()
			reporter := publicationReporter(func(_ ghstatus.Target, _ ghstatus.Status) error {
				cancelRun()
				if err := os.Remove(eventsPath); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(eventsPath, 0o755); err != nil {
					t.Fatal(err)
				}
				return nil
			})
			if stepPending {
				err = postStepPendingStatusDuringRun(ctx, reporter, &appender, run.Meta, run.StepStatuses[0])
			} else {
				err = postPendingAggregateStatus(ctx, reporter, &appender, run.Meta)
			}
			if err == nil || !strings.Contains(err.Error(), "receipt was not saved") {
				t.Fatalf("local receipt failure was swallowed during cancellation: %v", err)
			}
		})
	}
}

func TestParallelRunTimeoutFailsOnlyItsStep(t *testing.T) {
	t.Parallel()
	plan := config.ResolvedPlan{Steps: []config.Step{
		{ID: "timeout", Timeout: "50ms", Command: []string{"sh", "-c", "sleep 30"}},
		{ID: "sibling", Command: []string{"sh", "-c", "printf completed"}},
		{ID: "blocked", Needs: []string{"timeout"}, Command: []string{"sh", "-c", "exit 99"}},
	}}
	plan.ApplyDefaults()
	fixture := newRunFixture(t, plan)
	run := prepareRunFixture(t, fixture)
	run.Meta.MaxParallel = 2
	results, _ := getAsyncRun(t, fixture, run, ExecuteOptions{})
	result := getFinishedRun(t, results)
	if result.Summary.Status != "failure" || result.StepStatuses[1].State != "success" || result.StepStatuses[2].State != "blocked" {
		t.Fatalf("summary = %s, states = %v", result.Summary.Status, statusStates(result.StepStatuses))
	}
	status := result.StepStatuses[0]
	if status.State != "failure" || status.ExitCode != nil || status.Timeout != "50ms" || status.Message != "timeout after 50ms" {
		t.Fatalf("timeout status = %+v", status)
	}
	if !strings.Contains(mustReadFile(t, fixture.store.StepFile(run.RunID, 0, "timeout", persistence.CombinedLog)), status.Message) {
		t.Fatal("timeout message missing from log")
	}
	if _, err := LoadRun(fixture.store, run.RunID); err != nil {
		t.Fatal(err)
	}
}

func TestParallelRunForceStopReachesEveryWorkerAndResumes(t *testing.T) {
	t.Parallel()
	plan := config.ResolvedPlan{Steps: []config.Step{
		// Builtin markers and exec avoid a marker child holding pipes during startup cancellation.
		{ID: "first", Command: []string{"sh", "-c", "[ -f allow-finish ] && exit 0; trap '' TERM; : > first-started; exec sleep 30"}},
		{ID: "second", Command: []string{"sh", "-c", "[ -f allow-finish ] && exit 0; trap '' TERM; : > second-started; exec sleep 30"}},
		{ID: "after", Needs: []string{"first", "second"}, Command: []string{"sh", "-c", "exit 0"}},
	}}
	plan.ApplyDefaults()
	fixture := newRunFixture(t, plan)
	run := prepareRunFixture(t, fixture)
	run.Meta.MaxParallel = 2
	forceStop := make(chan struct{})
	results, cancelRun := getAsyncRun(t, fixture, run, ExecuteOptions{ForceStop: forceStop})
	validateMarkerExists(t, filepath.Join(fixture.repoRoot, "first-started"))
	validateMarkerExists(t, filepath.Join(fixture.repoRoot, "second-started"))
	cancelRun()
	close(forceStop)
	var interrupted RunRecord
	select {
	case result := <-results:
		if result.err != nil {
			t.Fatal(result.err)
		}
		interrupted = result.run
	case <-time.After(time.Second):
		t.Fatal("force-stop broadcast did not bypass the two-second grace period")
	}
	states := statusStates(interrupted.StepStatuses)
	if interrupted.Summary.Status != "interrupted" || !interrupted.Meta.Interrupted ||
		states["first"] != "interrupted" || states["second"] != "interrupted" || states["after"] != "pending" {
		t.Fatalf("summary = %s, interrupted = %t, states = %v", interrupted.Summary.Status, interrupted.Meta.Interrupted, states)
	}

	writeFile(t, filepath.Join(fixture.repoRoot, "allow-finish"), nil)
	loaded, err := LoadRunForResume(fixture.store, run.RunID, fixture.identity)
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := ExecuteRun(t.Context(), fixture.store, loaded, ExecuteOptions{})
	if err != nil || resumed.Summary.Status != "success" || resumed.Meta.Interrupted {
		t.Fatalf("resume = %s, interrupted = %t, error = %v", resumed.Summary.Status, resumed.Meta.Interrupted, err)
	}
}

func TestParallelRunPersistenceErrorStopsAndJoinsActiveWorkers(t *testing.T) {
	t.Parallel()
	plan := config.ResolvedPlan{Steps: []config.Step{
		{ID: "break-summary", Command: []string{"sh", "-c", "while [ ! -f sibling-started ]; do sleep 0.02; done; rm \"$LOCAL_CI_RUN_DIR/summary.json\"; mkdir \"$LOCAL_CI_RUN_DIR/summary.json\""}},
		// The sibling records that it was stopped only after a delay, so the marker exists on
		// return only if ExecuteRun canceled the sibling and then waited for it to exit.
		{ID: "sibling", Command: []string{"sh", "-c", "trap 'sleep 0.5; : > sibling-stopped; exit 0' TERM; : > sibling-started; while :; do sleep 0.05; done"}},
	}}
	plan.ApplyDefaults()
	fixture := newRunFixture(t, plan)
	run := prepareRunFixture(t, fixture)
	run.Meta.MaxParallel = 2
	results, _ := getAsyncRun(t, fixture, run, ExecuteOptions{})
	select {
	case result := <-results:
		if result.err == nil || !strings.Contains(result.err.Error(), "summary.json") {
			t.Fatalf("error = %v, want local persistence failure", result.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("local persistence error did not cancel the active sibling")
	}
	if _, err := os.Stat(filepath.Join(fixture.repoRoot, "sibling-stopped")); err != nil {
		t.Fatal("ExecuteRun returned before joining the canceled sibling")
	}
}

func TestParallelRunLogFailureIsFatalLocalError(t *testing.T) {
	t.Parallel()
	// Write failures need a full device; Linux release CI runs that row.
	for _, failurePoint := range []string{"open", "write"} {
		t.Run(failurePoint, func(t *testing.T) {
			if failurePoint == "write" {
				if _, err := os.Stat("/dev/full"); err != nil {
					t.Skip("write-failure device /dev/full is unavailable")
				}
			}
			plan := config.ResolvedPlan{Steps: []config.Step{
				{ID: "broken-log", Command: []string{"sh", "-c", "printf output; exit 7"}},
				{ID: "sibling", Command: []string{"sh", "-c", "sleep 30"}},
			}}
			plan.ApplyDefaults()
			fixture := newRunFixtureWithGitHub(t, plan, config.GitHub{Enabled: true})
			run := prepareRunFixture(t, fixture)
			run.Meta.MaxParallel = 2
			logPath := fixture.store.StepFile(run.RunID, 0, "broken-log", persistence.StdoutLog)
			reporter := publicationReporter(func(_ ghstatus.Target, status ghstatus.Status) error {
				if status.Context != "local/broken-log" || status.State != ghstatus.StatePending {
					return nil
				}
				if err := os.Remove(logPath); err != nil {
					t.Fatal(err)
				}
				if failurePoint == "open" {
					if err := os.Mkdir(logPath, 0o755); err != nil {
						t.Fatal(err)
					}
				} else if err := os.Symlink("/dev/full", logPath); err != nil {
					t.Fatal(err)
				}
				return nil
			})
			results, _ := getAsyncRun(t, fixture, run, ExecuteOptions{Reporter: reporter})
			select {
			case result := <-results:
				if !errors.Is(result.err, errStepLogWrite) || !strings.Contains(result.err.Error(), logPath) {
					t.Fatalf("log persistence failure was treated as a check result: %v", result.err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("log persistence failure did not cancel the sibling")
			}
		})
	}
}

func TestFailureProgressDoesNotWaitForRemoteReporting(t *testing.T) {
	t.Parallel()
	plan := config.ResolvedPlan{Steps: []config.Step{{ID: "fail", Command: []string{"sh", "-c", "exit 7"}}}}
	plan.ApplyDefaults()
	fixture := newRunFixtureWithGitHub(t, plan, config.GitHub{Enabled: true})
	run := prepareRunFixture(t, fixture)
	var progress bytes.Buffer
	logPath := fixture.store.StepFile(run.RunID, 0, "fail", persistence.CombinedLog)
	reporter := publicationReporter(func(_ ghstatus.Target, status ghstatus.Status) error {
		if status.Context == "local/fail" && status.State == ghstatus.StateFailure && !strings.Contains(progress.String(), "log "+logPath) {
			t.Error("failure log path was not printed before the remote post")
		}
		return nil
	})
	if _, err := ExecuteRun(t.Context(), fixture.store, run, ExecuteOptions{Reporter: reporter, Progress: &progress}); err != nil {
		t.Fatal(err)
	}
}

func TestCanceledRunDoesNotLaunchUnstartedWork(t *testing.T) {
	t.Parallel()
	plan := config.ResolvedPlan{Steps: []config.Step{{ID: "test", Command: []string{"sh", "-c", "touch should-not-exist"}}}}
	plan.ApplyDefaults()
	fixture := newRunFixture(t, plan)
	run := prepareRunFixture(t, fixture)
	ctx, cancelRun := context.WithCancel(t.Context())
	cancelRun()
	result, err := ExecuteRun(ctx, fixture.store, run, ExecuteOptions{})
	if err != nil || result.Summary.Status != "interrupted" || result.StepStatuses[0].State != "pending" {
		t.Fatalf("result = %+v, error = %v", result.Summary, err)
	}
	if _, err := LoadRun(fixture.store, run.RunID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(fixture.repoRoot, "should-not-exist")); !os.IsNotExist(err) {
		t.Fatal("canceled run launched a command")
	}
}

func TestRerunFromStepMarksDependentsAndReusesUnrelatedSuccess(t *testing.T) {
	t.Parallel()
	plan := config.ResolvedPlan{Steps: []config.Step{
		{ID: "first", Command: []string{"sh", "-c", "printf first\\n >> runs.log"}},
		{ID: "after", Needs: []string{"first"}, Command: []string{"sh", "-c", "printf after\\n >> runs.log"}},
		{ID: "unrelated", Command: []string{"sh", "-c", "printf unrelated\\n >> runs.log"}},
	}}
	plan.ApplyDefaults()
	fixture := newRunFixtureWithGitHub(t, plan, config.GitHub{Enabled: true})
	run := prepareRunFixture(t, fixture)
	completed, err := ExecuteRun(t.Context(), fixture.store, run, ExecuteOptions{Reporter: &fakeReporter{}})
	if err != nil {
		t.Fatal(err)
	}
	reporter := &fakeReporter{}
	rerun, err := ExecuteRun(t.Context(), fixture.store, completed, ExecuteOptions{FromStep: "first", Reporter: reporter})
	if err != nil || rerun.Summary.Status != "success" {
		t.Fatalf("rerun error = %v", err)
	}
	log := mustReadFile(t, filepath.Join(fixture.repoRoot, "runs.log"))
	if strings.Count(log, "first") != 2 || strings.Count(log, "after") != 2 || strings.Count(log, "unrelated") != 1 {
		t.Fatalf("execution log = %q", log)
	}
	staleCount := 0
	for _, event := range mustReadEvents(t, fixture.store.RunFile(run.RunID, persistence.EventsFile)) {
		if event.Type == events.StepStale {
			staleCount++
		}
	}
	if staleCount != 2 {
		t.Fatalf("stale events = %d", staleCount)
	}
	// A dependent's old success must turn pending before the rerun starts, not when it is reached.
	posts := make([]string, 0, len(reporter.posts))
	for _, post := range reporter.posts {
		posts = append(posts, post.status.Context+"="+string(post.status.State))
	}
	afterPending, firstSuccess := slices.Index(posts, "local/after=pending"), slices.Index(posts, "local/first=success")
	if afterPending < 0 || firstSuccess < 0 || afterPending > firstSuccess {
		t.Fatalf("stale dependent was not refreshed before the rerun: %v", posts)
	}
}

func TestInterruptedRunReportingStopsAtTheShutdownBudget(t *testing.T) {
	t.Parallel()
	// Waits out the real five-second budget: a hung status post must not block shutdown forever.
	plan := config.ResolvedPlan{Steps: []config.Step{{ID: "test", Command: []string{"sh", "-c", ": > started; exec sleep 30"}}}}
	plan.ApplyDefaults()
	fixture := newRunFixtureWithGitHub(t, plan, config.GitHub{Enabled: true})
	run := prepareRunFixture(t, fixture)
	reporter := contextReporter(func(reportContext context.Context, _ ghstatus.Target, status ghstatus.Status) error {
		if status.State != ghstatus.StateError {
			return nil
		}
		<-reportContext.Done()
		return reportContext.Err()
	})
	results, cancelRun := getAsyncRun(t, fixture, run, ExecuteOptions{Reporter: reporter})
	validateMarkerExists(t, filepath.Join(fixture.repoRoot, "started"))
	cancelRun()
	select {
	case result := <-results:
		if result.err != nil || result.run.Summary.Status != "interrupted" {
			t.Fatalf("summary = %s, error = %v", result.run.Summary.Status, result.err)
		}
	case <-time.After(finalReportTimeout + 3*time.Second):
		t.Fatal("a hung status post outlived the shutdown reporting budget")
	}
}

func TestClassifyStepState(t *testing.T) {
	t.Parallel()
	expiredCommand, cancelCommand := context.WithTimeout(t.Context(), 0)
	defer cancelCommand()
	canceledRun, cancelRun := context.WithCancel(t.Context())
	cancelRun()
	exitError := errors.New("exit status 7")
	for _, test := range []struct {
		name         string
		runContext   context.Context
		runError     error
		wantState    StepState
		wantTimedOut bool
	}{
		// The deadline can pass while results are read after a clean exit.
		{name: "clean exit after the deadline", runContext: t.Context(), wantState: StepStateSuccess},
		{name: "failure after the deadline", runContext: t.Context(), runError: exitError, wantState: StepStateFailure, wantTimedOut: true},
		{name: "operator interruption", runContext: canceledRun, runError: exitError, wantState: StepStateInterrupted},
	} {
		t.Run(test.name, func(t *testing.T) {
			state, timedOut := classifyStepState(test.runContext, expiredCommand, test.runError)
			if state != test.wantState || timedOut != test.wantTimedOut {
				t.Fatalf("state = %s, timed out = %t", state, timedOut)
			}
		})
	}
}

type contextReporter func(context.Context, ghstatus.Target, ghstatus.Status) error

func (reporter contextReporter) PostStatus(reportContext context.Context, target ghstatus.Target, status ghstatus.Status) error {
	return reporter(reportContext, target, status)
}

// executeSuppressedRun executes a run prepared like `run --no-github`, the only kind that publishes.
func executeSuppressedRun(t *testing.T, fixture runFixture) RunRecord {
	t.Helper()
	run := prepareSuppressedRunFixture(t, fixture, "cli_disabled")
	run.Meta.MaxParallel = 2
	completed, err := ExecuteRun(t.Context(), fixture.store, run, ExecuteOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return completed
}

type asyncRunResult struct {
	run RunRecord
	err error
}

func getAsyncRun(t *testing.T, fixture runFixture, run RunRecord, opts ExecuteOptions) (<-chan asyncRunResult, context.CancelFunc) {
	t.Helper()
	ctx, cancelRun := context.WithCancel(t.Context())
	results := make(chan asyncRunResult, 1)
	finished := make(chan struct{})
	go func() {
		result, err := ExecuteRun(ctx, fixture.store, run, opts)
		results <- asyncRunResult{run: result, err: err}
		close(finished)
	}()
	t.Cleanup(func() {
		cancelRun()
		select {
		case <-finished:
		case <-time.After(10 * time.Second):
			t.Error("run did not stop during test cleanup")
		}
	})
	return results, cancelRun
}

func getFinishedRun(t *testing.T, results <-chan asyncRunResult) RunRecord {
	t.Helper()
	select {
	case result := <-results:
		if result.err != nil {
			t.Fatal(result.err)
		}
		return result.run
	case <-time.After(10 * time.Second):
		t.Fatal("run did not finish")
		return RunRecord{}
	}
}

func validateMarkerExists(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("marker did not appear: %s", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func validateSharedContextNeverPasses(t *testing.T, reporter *fakeReporter) {
	t.Helper()
	lastState := ghstatus.StatePending
	for _, post := range reporter.posts {
		if post.status.Context != "shared" {
			continue
		}
		if post.status.State == ghstatus.StateSuccess {
			t.Fatal("shared context passed despite a failed check")
		}
		lastState = post.status.State
	}
	if lastState != ghstatus.StateFailure {
		t.Fatalf("shared context final state = %s", lastState)
	}
}

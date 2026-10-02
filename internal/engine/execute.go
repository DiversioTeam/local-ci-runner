package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/DiversioTeam/local-ci-runner/internal/config"
	"github.com/DiversioTeam/local-ci-runner/internal/events"
	ghstatus "github.com/DiversioTeam/local-ci-runner/internal/github"
	"github.com/DiversioTeam/local-ci-runner/internal/persistence"
)

const finalReportTimeout = 5 * time.Second

var errStepLogWrite = errors.New("save step logs")

type stepLogWriter struct {
	file          *os.File
	addWriteError func(error)
}

func (writer stepLogWriter) Write(output []byte) (int, error) {
	writtenBytes, writeError := writer.file.Write(output)
	if writeError != nil {
		logError := fmt.Errorf("%w: %w", errStepLogWrite, writeError)
		writer.addWriteError(logError)
		return writtenBytes, logError
	}
	return writtenBytes, nil
}

// ExecuteOptions configures one run execution.
type ExecuteOptions struct {
	// ForceStop requests immediate process-group termination when closed.
	ForceStop          <-chan struct{}
	Now                func() time.Time
	Reporter           ghstatus.Reporter
	Stdout             io.Writer
	Stderr             io.Writer
	Progress           io.Writer
	FromStep           string
	finalReportContext context.Context
}

// ExecuteRun executes unfinished plan steps and persists their lifecycle.
// If ctx is canceled during a step, ExecuteRun finalizes and returns an interrupted run.
func ExecuteRun(ctx context.Context, store persistence.Store, run RunRecord, opts ExecuteOptions) (RunRecord, error) {
	if err := validateStoredStepStatuses(run.Plan, run.StepStatuses); err != nil {
		return RunRecord{}, err
	}
	if err := validateStoredSummary(run.Meta, run.Summary, run.StepStatuses); err != nil {
		return RunRecord{}, err
	}

	if err := run.Plan.Validate(); err != nil {
		return RunRecord{}, err
	}
	if opts.FromStep != "" {
		statuses, err := MarkStaleFromStep(run.Plan, run.StepStatuses, opts.FromStep)
		if err != nil {
			return RunRecord{}, err
		}
		run.StepStatuses = statuses
	}
	order, err := executionOrder(run.Plan)
	if err != nil {
		return RunRecord{}, err
	}

	appender, err := events.NewAppender(store.RunFile(run.RunID, persistence.EventsFile), run.RunID)
	if err != nil {
		return RunRecord{}, err
	}

	// All terminal posts share one shutdown budget, even with many active steps.
	finalReportContext, cancelFinalReport := context.WithCancel(context.WithoutCancel(ctx))
	opts.finalReportContext = finalReportContext
	stopFinalReport := context.AfterFunc(ctx, func() {
		timer := time.NewTimer(finalReportTimeout)
		defer timer.Stop()
		select {
		case <-timer.C:
			cancelFinalReport()
		case <-finalReportContext.Done():
		}
	})
	defer stopFinalReport()
	defer cancelFinalReport()

	now := resolveNow(opts.Now)
	progress := opts.Progress
	if err := validateReporter(run.Meta, opts.Reporter); err != nil {
		return RunRecord{}, err
	}
	if run.Meta.StartedAt == nil {
		startedAt := now()
		run.Meta.StartedAt = &startedAt
	}
	run.Meta.FinishedAt = nil
	run.Meta.Interrupted = false
	run.Meta.MaxParallel = max(1, run.Meta.MaxParallel)
	runnerPID := os.Getpid()
	run.Meta.RunnerPID = &runnerPID
	if err := persistence.WriteJSONFile(store.RunFile(run.RunID, persistence.MetaFile), run.Meta); err != nil {
		return RunRecord{}, err
	}
	if err := persistSummary(store, &run, nil); err != nil {
		return RunRecord{}, err
	}
	runStartedAt := now()
	printProgress(progress, "run %s started\n", run.RunID)
	if err := appender.Append(runStartedAt, events.RunStarted, "", runStatusPending, ""); err != nil {
		return RunRecord{}, err
	}
	postError := postPendingAggregateStatus(ctx, opts.Reporter, &appender, run.Meta, runStartedAt)
	if err := tolerateGitHubPostFailure(store, &run, postError); err != nil {
		return RunRecord{}, err
	}

	if err := addStepsForExecution(ctx, store, &run, now, &appender, opts); err != nil {
		return RunRecord{}, err
	}
	if run.Meta.MaxParallel > 1 {
		printProgress(progress, "max_parallel=%d; child output stays in per-step logs; inspect local-ci show %s\n", run.Meta.MaxParallel, run.RunID)
		opts.Stdout, opts.Stderr = nil, nil
	}
	if err := executeReadySteps(ctx, store, &run, order, now, &appender, opts); err != nil {
		return RunRecord{}, err
	}

	finishedAt := now()
	run.Meta.Interrupted = ctx.Err() != nil
	run.Meta.FinishedAt = &finishedAt
	if err := persistence.WriteJSONFile(store.RunFile(run.RunID, persistence.MetaFile), run.Meta); err != nil {
		return RunRecord{}, err
	}
	if err := persistSummary(store, &run, &finishedAt); err != nil {
		return RunRecord{}, err
	}
	printProgress(progress, "run %s finished: %s\n", run.RunID, run.Summary.Status)
	if err := appender.Append(finishedAt, events.RunFinished, "", run.Summary.Status, ""); err != nil {
		return RunRecord{}, err
	}
	finalPostError := postFinalAggregateStatus(ctx, opts.Reporter, &appender, run.Meta, aggregateGitHubState(run.Summary.Status), finishedAt, opts.finalReportContext)
	if err := tolerateGitHubPostFailure(store, &run, finalPostError); err != nil {
		return RunRecord{}, err
	}

	return run, nil
}

func addStepStart(ctx context.Context, store persistence.Store, run *RunRecord, stepIndex int, now func() time.Time, appender *events.Appender, opts ExecuteOptions) error {
	step := run.Plan.Steps[stepIndex]
	startedAt := now()
	printProgress(opts.Progress, "start %s\n", step.ID)
	setRunningState(&run.StepStatuses[stepIndex], startedAt)
	if err := resetStepArtifacts(store, run.RunID, stepIndex, step.ID); err != nil {
		return err
	}
	if err := persistStepStatus(store, run.RunID, stepIndex, run.StepStatuses[stepIndex]); err != nil {
		return err
	}
	if err := persistSummary(store, run, nil); err != nil {
		return err
	}
	if err := appender.Append(startedAt, events.StepStarted, step.ID, string(StepStateRunning), ""); err != nil {
		return err
	}
	status := getContextStepStatus(run.StepStatuses, run.StepStatuses[stepIndex])
	if ctx.Err() != nil {
		return nil
	}
	postError := postStepStatusDuringRun(ctx, opts.Reporter, appender, run.Meta, status, startedAt, opts.finalReportContext)
	if err := tolerateGitHubPostFailure(store, run, postError); err != nil {
		return err
	}

	return nil
}

func addStepResult(ctx context.Context, store persistence.Store, run *RunRecord, result stepResult, appender *events.Appender, opts ExecuteOptions) error {
	stepIndex := result.stepIndex
	step := run.Plan.Steps[stepIndex]
	status := &run.StepStatuses[stepIndex]
	finishedAt := result.finishedAt
	state, exitCode, message := result.state, result.exitCode, result.message
	if message != "" {
		if err := appendRunnerMessage(store.StepFile(run.RunID, stepIndex, step.ID, persistence.StderrLog), message); err != nil {
			return err
		}
		if err := appendRunnerMessage(store.StepFile(run.RunID, stepIndex, step.ID, persistence.CombinedLog), message); err != nil {
			return err
		}
	}
	setCompletedState(status, state, *status.StartedAt, finishedAt, exitCode)
	status.Message = message
	if err := persistStepStatus(store, run.RunID, stepIndex, *status); err != nil {
		return err
	}
	if err := persistSummary(store, run, nil); err != nil {
		return err
	}
	if err := appender.Append(finishedAt, events.StepFinished, step.ID, string(state), message); err != nil {
		return err
	}
	// Report the saved local result before a remote request can delay progress.
	printProgress(opts.Progress, "%s %s\n", stateLabel(state), step.ID)
	if state == StepStateFailure || state == StepStateInterrupted {
		printProgress(opts.Progress, "log %s\n", store.StepFile(run.RunID, stepIndex, step.ID, persistence.CombinedLog))
	}
	contextStatus := getContextStepStatus(run.StepStatuses, *status)
	terminalPostError := postStepStatusDuringRun(ctx, opts.Reporter, appender, run.Meta, contextStatus, finishedAt, opts.finalReportContext)
	if err := tolerateGitHubPostFailure(store, run, terminalPostError); err != nil {
		return err
	}

	return nil
}

func runProcess(ctx context.Context, forceStop <-chan struct{}, store persistence.Store, run RunRecord, stepIndex int, step config.Step, stdout io.Writer, stderr io.Writer) (runError error, exitCode *int, message string) {
	stdoutPath := store.StepFile(run.RunID, stepIndex, step.ID, persistence.StdoutLog)
	stderrPath := store.StepFile(run.RunID, stepIndex, step.ID, persistence.StderrLog)
	combinedPath := store.StepFile(run.RunID, stepIndex, step.ID, persistence.CombinedLog)

	addLogCloseError := func(file *os.File) {
		if closeError := file.Close(); closeError != nil {
			runError = errors.Join(runError, fmt.Errorf("%w: %w", errStepLogWrite, closeError))
			message = runError.Error()
		}
	}
	stdoutFile, err := os.Create(stdoutPath)
	if err != nil {
		return fmt.Errorf("%w: %w", errStepLogWrite, err), nil, err.Error()
	}
	defer addLogCloseError(stdoutFile)
	stderrFile, err := os.Create(stderrPath)
	if err != nil {
		return fmt.Errorf("%w: %w", errStepLogWrite, err), nil, err.Error()
	}
	defer addLogCloseError(stderrFile)
	combinedFile, err := os.Create(combinedPath)
	if err != nil {
		return fmt.Errorf("%w: %w", errStepLogWrite, err), nil, err.Error()
	}
	defer addLogCloseError(combinedFile)

	commandContext, cancelCommand := context.WithCancel(ctx)
	defer cancelCommand()
	// Stdout and stderr copy concurrently; retain the first disk error safely.
	var firstLogWriteError error
	var logWriteErrorOnce sync.Once
	addLogWriteError := func(writeError error) {
		logWriteErrorOnce.Do(func() { firstLogWriteError = writeError })
		cancelCommand()
	}
	cmd := exec.CommandContext(commandContext, step.Command[0], step.Command[1:]...)
	cmd.Dir = resolveStepDir(run.Meta.RepoRoot, step.Dir)
	cmd.Env = stepEnv(run, stepIndex, step)
	stdoutLog := stepLogWriter{file: stdoutFile, addWriteError: addLogWriteError}
	stderrLog := stepLogWriter{file: stderrFile, addWriteError: addLogWriteError}
	combinedLog := stepLogWriter{file: combinedFile, addWriteError: addLogWriteError}
	cmd.Stdout = multiWriter(stdoutLog, combinedLog, stdout)
	cmd.Stderr = multiWriter(stderrLog, combinedLog, stderr)
	removeProcessCancellation := addProcessCancellation(commandContext, cmd, forceStop)
	defer removeProcessCancellation()

	err = cmd.Run()
	// Cmd.Run may prefer an exit error over its copy error; local log failures win.
	if firstLogWriteError != nil {
		return firstLogWriteError, nil, firstLogWriteError.Error()
	}
	if err == nil {
		commandExitCode := 0
		return nil, &commandExitCode, ""
	}
	var commandExitError *exec.ExitError
	if errors.As(err, &commandExitError) {
		commandExitCode := commandExitError.ExitCode()
		return err, &commandExitCode, fmt.Sprintf("exit code %d", commandExitCode)
	}
	return err, nil, err.Error()
}

// classifyStepState decides how a finished command is recorded. A passed deadline explains only a
// failed command: one that already exited cleanly stays successful even if its deadline then passed.
func classifyStepState(runContext context.Context, commandContext context.Context, runError error) (state StepState, timedOut bool) {
	switch {
	case runError == nil:
		return StepStateSuccess, false
	case runContext.Err() != nil:
		return StepStateInterrupted, false
	default:
		return StepStateFailure, errors.Is(commandContext.Err(), context.DeadlineExceeded)
	}
}

func getInterruptionMessage(runContext context.Context) string {
	if interruptionCause := context.Cause(runContext); interruptionCause != nil {
		return interruptionCause.Error()
	}
	return "run interrupted"
}

// tolerateGitHubPostFailure lets a run survive a failed status post.
//
// The local run is already the result that matters, so a remote failure is
// recorded and swallowed rather than thrown. Recording it also stops later
// posts, because githubPostingEnabled consults the same field — otherwise
// every remaining step would wait for its own timeout against a dead remote.
//
// Anything else, including a failure to write the local event log, is returned
// unchanged and ends the run.
func tolerateGitHubPostFailure(store persistence.Store, run *RunRecord, postError error) error {
	if !isGitHubPostFailure(postError) {
		return postError
	}

	run.Meta.GitHubPostingSuppressed = persistence.GitHubPostingSuppressionPostFailed
	if err := persistence.WriteJSONFile(store.RunFile(run.RunID, persistence.MetaFile), run.Meta); err != nil {
		return fmt.Errorf("persist GitHub posting suppression: %w", err)
	}
	if err := persistSummary(store, run, run.Meta.FinishedAt); err != nil {
		return fmt.Errorf("persist summary after GitHub posting failure: %w", err)
	}
	return nil
}

func postPendingAggregateStatus(
	runContext context.Context,
	reporter ghstatus.Reporter,
	appender *events.Appender,
	meta persistence.Meta,
	at time.Time,
) error {
	if runContext.Err() != nil {
		return nil
	}
	postError := postAggregateStatus(runContext, reporter, appender, meta, ghstatus.StatePending, at)
	if isGitHubPostFailure(postError) && runContext.Err() != nil {
		return nil
	}
	return postError
}

func postStepPendingStatusDuringRun(
	runContext context.Context,
	reporter ghstatus.Reporter,
	appender *events.Appender,
	meta persistence.Meta,
	status persistence.StepStatus,
	at time.Time,
) error {
	if runContext.Err() != nil {
		return nil
	}
	postError := postStepPendingStatus(runContext, reporter, appender, meta, status, at)
	if isGitHubPostFailure(postError) && runContext.Err() != nil {
		return nil
	}
	return postError
}

// A running step can share a context with a failed sibling; post the combined
// context state, not necessarily pending or the triggering step's own state.
func postStepStatusDuringRun(
	runContext context.Context,
	reporter ghstatus.Reporter,
	appender *events.Appender,
	meta persistence.Meta,
	status persistence.StepStatus,
	at time.Time,
	finalReportContext context.Context,
) error {
	return postTerminalStatusDuringRun(runContext, finalReportContext, func(reportContext context.Context) error {
		return postStepTerminalStatus(reportContext, reporter, appender, meta, status, at)
	})
}

func postFinalAggregateStatus(
	runContext context.Context,
	reporter ghstatus.Reporter,
	appender *events.Appender,
	meta persistence.Meta,
	state ghstatus.State,
	at time.Time,
	finalReportContext context.Context,
) error {
	return postTerminalStatusDuringRun(runContext, finalReportContext, func(reportContext context.Context) error {
		return postAggregateStatus(reportContext, reporter, appender, meta, state, at)
	})
}

func postTerminalStatusDuringRun(runContext, finalReportContext context.Context, postStatus func(context.Context) error) error {
	if runContext.Err() != nil {
		return postStatus(finalReportContext)
	}
	postError := postStatus(runContext)
	if postError == nil || runContext.Err() == nil {
		return postError
	}
	// Cancellation may justify another remote attempt, never another receipt write.
	if !isGitHubPostFailure(postError) && !errors.Is(postError, context.Canceled) {
		return postError
	}
	// Every caller supplies the same budget created by ExecuteRun.
	return postStatus(finalReportContext)
}

func executionOrder(plan config.ResolvedPlan) ([]int, error) {
	order := make([]int, 0, len(plan.Steps))
	added := make(map[string]struct{}, len(plan.Steps))
	used := make([]bool, len(plan.Steps))

	for len(order) < len(plan.Steps) {
		progressed := false
		for index, step := range plan.Steps {
			if used[index] || !dependenciesSatisfied(added, step.Needs) {
				continue
			}
			order = append(order, index)
			used[index] = true
			added[step.ID] = struct{}{}
			progressed = true
		}
		if !progressed {
			return nil, fmt.Errorf("execution order could not be resolved")
		}
	}

	return order, nil
}

func dependenciesSatisfied(done map[string]struct{}, needs []string) bool {
	for _, need := range needs {
		if _, ok := done[need]; !ok {
			return false
		}
	}
	return true
}

func blockedByDependencies(statuses []persistence.StepStatus, needs []string) (bool, string) {
	if len(needs) == 0 {
		return false, ""
	}

	stateByID := make(map[string]string, len(statuses))
	for _, status := range statuses {
		stateByID[status.StepID] = status.State
	}

	for _, need := range needs {
		if stateByID[need] != string(StepStateSuccess) && stateByID[need] != string(StepStateSkipped) {
			return true, fmt.Sprintf("blocked by %s=%s", need, stateByID[need])
		}
	}

	return false, ""
}

func shouldSkipStep(step config.Step) bool {
	return step.If == "false"
}

func resolveNow(now func() time.Time) func() time.Time {
	if now != nil {
		var clockLock sync.Mutex
		return func() time.Time {
			clockLock.Lock()
			defer clockLock.Unlock()
			return now().UTC()
		}
	}
	return func() time.Time { return time.Now().UTC() }
}

func resolveStepDir(repoRoot string, dir string) string {
	if filepath.IsAbs(dir) {
		return dir
	}
	return filepath.Join(repoRoot, dir)
}

func stepEnv(run RunRecord, stepIndex int, step config.Step) []string {
	env := make(map[string]string)
	for _, item := range os.Environ() {
		key, value, ok := strings.Cut(item, "=")
		if !ok {
			continue
		}
		env[key] = value
	}
	for key, value := range run.Plan.Env {
		env[key] = value
	}
	for key, value := range step.Env {
		env[key] = value
	}

	env["LOCAL_CI_RUN_ID"] = run.RunID
	env["LOCAL_CI_REPO_ROOT"] = run.Meta.RepoRoot
	env["LOCAL_CI_CONFIG"] = run.Meta.ConfigPath
	env["LOCAL_CI_RUN_DIR"] = run.RunDir
	env["LOCAL_CI_PLAN_FILE"] = filepath.Join(run.RunDir, persistence.PlanFile)
	env["LOCAL_CI_PLAN_ENV"] = filepath.Join(run.RunDir, persistence.PlanEnvFile)
	env["LOCAL_CI_GITHUB_REPO"] = run.Meta.RepoSlug
	env["LOCAL_CI_GITHUB_SHA"] = run.Meta.HeadSHA
	env["LOCAL_CI_STEP_ID"] = step.ID
	env["LOCAL_CI_STEP_NAME"] = step.Name
	env["LOCAL_CI_STEP_INDEX"] = fmt.Sprintf("%d", stepIndex+1)
	env["LOCAL_CI_STEP_DIR"] = storeStepDir(run.RunDir, stepIndex, step.ID)
	env["LOCAL_CI_STEP_OUTPUT"] = storeStepFile(run.RunDir, stepIndex, step.ID, persistence.OutputEnv)

	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+env[key])
	}
	return result
}

func storeStepDir(runDir string, stepIndex int, stepID string) string {
	return filepath.Join(runDir, persistence.StepRelDir(stepIndex, stepID))
}

func storeStepFile(runDir string, stepIndex int, stepID string, name string) string {
	return filepath.Join(runDir, persistence.StepRelPath(stepIndex, stepID, name))
}

func resetStepArtifacts(store persistence.Store, runID string, stepIndex int, stepID string) error {
	for _, name := range []string{persistence.StdoutLog, persistence.StderrLog, persistence.CombinedLog, persistence.OutputEnv} {
		if err := persistence.WriteTextFile(store.StepFile(runID, stepIndex, stepID, name), ""); err != nil {
			return err
		}
	}
	return nil
}

func multiWriter(writers ...io.Writer) io.Writer {
	active := make([]io.Writer, 0, len(writers))
	for _, writer := range writers {
		if writer != nil {
			active = append(active, writer)
		}
	}
	switch len(active) {
	case 0:
		return io.Discard
	case 1:
		return active[0]
	default:
		return io.MultiWriter(active...)
	}
}

func printProgress(writer io.Writer, format string, args ...any) {
	if writer == nil {
		return
	}
	_, _ = fmt.Fprintf(writer, format, args...)
}

func stateLabel(state StepState) string {
	switch state {
	case StepStateSuccess:
		return "ok"
	case StepStateFailure:
		return "fail"
	default:
		return string(state)
	}
}

func appendRunnerMessage(path string, message string) error {
	if message == "" {
		return nil
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer func() {
		_ = file.Close()
	}()
	if _, err := file.WriteString(message + "\n"); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func persistStepStatus(store persistence.Store, runID string, stepIndex int, status persistence.StepStatus) error {
	return persistence.WriteJSONFile(store.StepFile(runID, stepIndex, status.StepID, persistence.StatusFile), status)
}

func persistSummary(store persistence.Store, run *RunRecord, finishedAt *time.Time) error {
	meta := run.Meta
	meta.FinishedAt = finishedAt
	run.Summary = BuildRunSummary(meta, run.StepStatuses)
	if err := persistence.WriteJSONFile(store.RunFile(run.RunID, persistence.SummaryFile), run.Summary); err != nil {
		return err
	}
	if err := persistence.WriteTextFile(store.RunFile(run.RunID, persistence.SummaryText), renderSummaryText(run.RunDir, run.Meta, run.Summary, run.StepStatuses)); err != nil {
		return err
	}
	return nil
}

func setRunningState(status *persistence.StepStatus, startedAt time.Time) {
	status.State = string(StepStateRunning)
	status.Message = ""
	status.StartedAt = &startedAt
	status.FinishedAt = nil
	status.DurationMillis = 0
	status.ExitCode = nil
}

func setCompletedState(status *persistence.StepStatus, state StepState, startedAt time.Time, finishedAt time.Time, exitCode *int) {
	status.State = string(state)
	status.StartedAt = &startedAt
	status.FinishedAt = &finishedAt
	status.DurationMillis = finishedAt.Sub(startedAt).Milliseconds()
	status.ExitCode = exitCode
}

func setTerminalState(status *persistence.StepStatus, state StepState, at time.Time, exitCode *int) {
	status.State = string(state)
	status.StartedAt = &at
	status.FinishedAt = &at
	status.DurationMillis = 0
	status.ExitCode = exitCode
}

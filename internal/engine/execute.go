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
	"time"

	"github.com/DiversioTeam/local-ci-runner/internal/config"
	"github.com/DiversioTeam/local-ci-runner/internal/events"
	ghstatus "github.com/DiversioTeam/local-ci-runner/internal/github"
	"github.com/DiversioTeam/local-ci-runner/internal/persistence"
)

type ExecuteOptions struct {
	Context  context.Context
	Now      func() time.Time
	Reporter ghstatus.Reporter
	Stdout   io.Writer
	Stderr   io.Writer
	Progress io.Writer
}

func ExecuteRun(store persistence.Store, run RunRecord, opts ExecuteOptions) (RunRecord, error) {
	if err := validateStoredStepStatuses(run.Plan, run.StepStatuses); err != nil {
		return RunRecord{}, err
	}
	if err := validateStoredSummary(run.Meta, run.Summary, run.StepStatuses); err != nil {
		return RunRecord{}, err
	}

	order, err := executionOrder(run.Plan)
	if err != nil {
		return RunRecord{}, err
	}

	appender, err := events.NewAppender(store.RunFile(run.RunID, persistence.EventsFile), run.RunID)
	if err != nil {
		return RunRecord{}, err
	}

	now := resolveNow(opts.Now)
	ctx := resolveContext(opts.Context)
	stdout, stderr, progress := resolveWriters(opts)
	if err := validateReporter(run.Meta, opts.Reporter); err != nil {
		return RunRecord{}, err
	}
	if run.Meta.StartedAt == nil {
		startedAt := now()
		run.Meta.StartedAt = &startedAt
	}
	run.Meta.FinishedAt = nil
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
	if err := postAggregateStatus(ctx, opts.Reporter, &appender, run.Meta, ghstatus.StatePending, runStartedAt); err != nil {
		return RunRecord{}, err
	}

	for _, stepIndex := range order {
		if run.StepStatuses[stepIndex].State == string(StepStateSuccess) {
			continue
		}

		step := run.Plan.Steps[stepIndex]
		if shouldSkipStep(step) {
			at := now()
			printProgress(progress, "skip %s (condition=false)\n", step.ID)
			setTerminalState(&run.StepStatuses[stepIndex], StepStateSkipped, at, nil)
			if err := persistStepStatus(store, run.RunID, stepIndex, run.StepStatuses[stepIndex]); err != nil {
				return RunRecord{}, err
			}
			if err := persistSummary(store, &run, nil); err != nil {
				return RunRecord{}, err
			}
			if err := appender.Append(at, events.StepSkipped, step.ID, string(StepStateSkipped), "condition=false"); err != nil {
				return RunRecord{}, err
			}
			if err := postStepTerminalStatus(ctx, opts.Reporter, &appender, run.Meta, run.StepStatuses[stepIndex], at); err != nil {
				return RunRecord{}, err
			}
			continue
		}

		if blocked, message := blockedByDependencies(run.StepStatuses, step.Needs); blocked {
			at := now()
			printProgress(progress, "blocked %s (%s)\n", step.ID, message)
			setTerminalState(&run.StepStatuses[stepIndex], StepStateBlocked, at, nil)
			if err := persistStepStatus(store, run.RunID, stepIndex, run.StepStatuses[stepIndex]); err != nil {
				return RunRecord{}, err
			}
			if err := persistSummary(store, &run, nil); err != nil {
				return RunRecord{}, err
			}
			if err := appender.Append(at, events.StepBlocked, step.ID, string(StepStateBlocked), message); err != nil {
				return RunRecord{}, err
			}
			if err := postStepTerminalStatus(ctx, opts.Reporter, &appender, run.Meta, run.StepStatuses[stepIndex], at); err != nil {
				return RunRecord{}, err
			}
			continue
		}

		if err := executeStep(ctx, store, &run, stepIndex, step, now, &appender, opts.Reporter, stdout, stderr, progress); err != nil {
			return RunRecord{}, err
		}
	}

	finishedAt := now()
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
	if err := postAggregateStatus(ctx, opts.Reporter, &appender, run.Meta, aggregateGitHubState(run.Summary.Status), finishedAt); err != nil {
		return RunRecord{}, err
	}

	return run, nil
}

func executeStep(
	ctx context.Context,
	store persistence.Store,
	run *RunRecord,
	stepIndex int,
	step config.Step,
	now func() time.Time,
	appender *events.Appender,
	reporter ghstatus.Reporter,
	stdout io.Writer,
	stderr io.Writer,
	progress io.Writer,
) error {
	startedAt := now()
	printProgress(progress, "start %s\n", step.ID)
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
	if err := postStepPendingStatus(ctx, reporter, appender, run.Meta, run.StepStatuses[stepIndex], startedAt); err != nil {
		return err
	}

	runErr, exitCode, message := runProcess(ctx, store, *run, stepIndex, step, stdout, stderr)
	if runErr == nil {
		if _, envErr := persistence.ReadEnvFile(store.StepFile(run.RunID, stepIndex, step.ID, persistence.OutputEnv)); envErr != nil {
			runErr = envErr
			message = envErr.Error()
		}
	}

	finishedAt := now()
	state := StepStateSuccess
	if runErr != nil {
		state = StepStateFailure
		if message == "" {
			message = runErr.Error()
		}
		if err := appendRunnerMessage(store.StepFile(run.RunID, stepIndex, step.ID, persistence.StderrLog), message); err != nil {
			return err
		}
		if err := appendRunnerMessage(store.StepFile(run.RunID, stepIndex, step.ID, persistence.CombinedLog), message); err != nil {
			return err
		}
	}
	setCompletedState(&run.StepStatuses[stepIndex], state, startedAt, finishedAt, exitCode)
	if err := persistStepStatus(store, run.RunID, stepIndex, run.StepStatuses[stepIndex]); err != nil {
		return err
	}
	if err := persistSummary(store, run, nil); err != nil {
		return err
	}
	if err := appender.Append(finishedAt, events.StepFinished, step.ID, string(state), message); err != nil {
		return err
	}
	if err := postStepTerminalStatus(ctx, reporter, appender, run.Meta, run.StepStatuses[stepIndex], finishedAt); err != nil {
		return err
	}
	printProgress(progress, "%s %s\n", stateLabel(state), step.ID)
	if state == StepStateFailure {
		printProgress(progress, "log %s\n", store.StepFile(run.RunID, stepIndex, step.ID, persistence.CombinedLog))
	}

	return nil
}

func runProcess(ctx context.Context, store persistence.Store, run RunRecord, stepIndex int, step config.Step, stdout io.Writer, stderr io.Writer) (error, *int, string) {
	stdoutPath := store.StepFile(run.RunID, stepIndex, step.ID, persistence.StdoutLog)
	stderrPath := store.StepFile(run.RunID, stepIndex, step.ID, persistence.StderrLog)
	combinedPath := store.StepFile(run.RunID, stepIndex, step.ID, persistence.CombinedLog)

	stdoutFile, err := os.Create(stdoutPath)
	if err != nil {
		return err, nil, err.Error()
	}
	defer func() {
		_ = stdoutFile.Close()
	}()
	stderrFile, err := os.Create(stderrPath)
	if err != nil {
		return err, nil, err.Error()
	}
	defer func() {
		_ = stderrFile.Close()
	}()
	combinedFile, err := os.Create(combinedPath)
	if err != nil {
		return err, nil, err.Error()
	}
	defer func() {
		_ = combinedFile.Close()
	}()

	cmd := exec.CommandContext(ctx, step.Command[0], step.Command[1:]...)
	cmd.Dir = resolveStepDir(run.Meta.RepoRoot, step.Dir)
	cmd.Env = stepEnv(run, stepIndex, step)
	cmd.Stdout = multiWriter(stdoutFile, combinedFile, stdout)
	cmd.Stderr = multiWriter(stderrFile, combinedFile, stderr)

	err = cmd.Run()
	switch {
	case err == nil:
		exitCode := 0
		return nil, &exitCode, ""
	default:
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode := exitErr.ExitCode()
			return err, &exitCode, fmt.Sprintf("exit code %d", exitCode)
		}
		return err, nil, err.Error()
	}
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
		if stateByID[need] != string(StepStateSuccess) {
			return true, fmt.Sprintf("blocked by %s=%s", need, stateByID[need])
		}
	}

	return false, ""
}

func shouldSkipStep(step config.Step) bool {
	return step.If == "false"
}

func resolveContext(ctx context.Context) context.Context {
	if ctx != nil {
		return ctx
	}
	return context.Background()
}

func resolveWriters(opts ExecuteOptions) (io.Writer, io.Writer, io.Writer) {
	return opts.Stdout, opts.Stderr, opts.Progress
}

func resolveNow(now func() time.Time) func() time.Time {
	if now != nil {
		return func() time.Time { return now().UTC() }
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
	run.Summary = buildSummary(run.RunID, run.StepStatuses, run.Meta.StartedAt, finishedAt)
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

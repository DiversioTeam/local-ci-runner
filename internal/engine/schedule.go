package engine

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/DiversioTeam/local-ci-runner/internal/events"
	"github.com/DiversioTeam/local-ci-runner/internal/persistence"
)

type stepResult struct {
	stepIndex     int
	state         StepState
	exitCode      *int
	message       string
	finishedAt    time.Time
	artifactError error
}

func addStepsForExecution(ctx context.Context, store persistence.Store, run *RunRecord, now func() time.Time, appender *events.Appender, opts ExecuteOptions) error {
	for stepIndex, status := range run.StepStatuses {
		if status.State == string(StepStateSuccess) {
			printProgress(opts.Progress, "reuse %s (success)\n", status.StepID)
			continue
		}
		if opts.FromStep != "" && status.State == string(StepStateStale) {
			at := now()
			if err := appender.Append(at, events.StepStale, status.StepID, string(StepStateStale), "rerun from "+opts.FromStep); err != nil {
				return err
			}
			postError := postStepPendingStatusDuringRun(ctx, opts.Reporter, appender, run.Meta, status, at)
			if err := tolerateGitHubPostFailure(store, run, postError); err != nil {
				return err
			}
		}
		// Old failures must not block a dependency that is about to be rerun.
		run.StepStatuses[stepIndex] = expectedStepStatus(run.Plan.Steps[stepIndex], stepIndex)
		if err := persistStepStatus(store, run.RunID, stepIndex, run.StepStatuses[stepIndex]); err != nil {
			return err
		}
	}
	return persistSummary(store, run, nil)
}

func executeReadySteps(
	ctx context.Context,
	store persistence.Store,
	run *RunRecord,
	stepOrder []int,
	now func() time.Time,
	appender *events.Appender,
	opts ExecuteOptions,
) error {
	workerContext, cancelWorkers := context.WithCancel(ctx)
	workerResults := make(chan stepResult, min(run.Meta.MaxParallel, len(stepOrder)))
	activeWorkerCount := 0
	defer func() {
		// Artifact failures are fatal: cancel and join every remaining worker.
		cancelWorkers()
		for activeWorkerCount > 0 {
			<-workerResults
			activeWorkerCount--
		}
	}()

	// Dependencies become ready only after their results have been saved.
	completedSteps := make(map[string]struct{}, len(stepOrder))
	for _, status := range run.StepStatuses {
		if status.State == string(StepStateSuccess) {
			completedSteps[status.StepID] = struct{}{}
		}
	}
	// ponytail: scan the small plan after each completion; index ready steps if scheduling becomes measurable.
	for len(completedSteps) < len(stepOrder) {
		for _, stepIndex := range stepOrder {
			if ctx.Err() != nil || activeWorkerCount >= run.Meta.MaxParallel {
				break
			}
			step := run.Plan.Steps[stepIndex]
			// Preparation resets unfinished steps to pending; running steps cannot launch twice.
			if run.StepStatuses[stepIndex].State != string(StepStatePending) || !dependenciesSatisfied(completedSteps, step.Needs) {
				continue
			}
			if shouldSkipStep(step) {
				if err := addUnexecutedStep(ctx, store, run, stepIndex, StepStateSkipped, "condition=false", now(), appender, opts); err != nil {
					return err
				}
				completedSteps[step.ID] = struct{}{}
				continue
			}
			if blocked, message := blockedByDependencies(run.StepStatuses, step.Needs); blocked {
				if err := addUnexecutedStep(ctx, store, run, stepIndex, StepStateBlocked, message, now(), appender, opts); err != nil {
					return err
				}
				completedSteps[step.ID] = struct{}{}
				continue
			}
			if err := addStepStart(ctx, store, run, stepIndex, now, appender, opts); err != nil {
				return err
			}
			activeWorkerCount++
			// Workers receive immutable identity and plan data, never shared statuses.
			commandRun := RunRecord{
				RunID:  run.RunID,
				RunDir: run.RunDir,
				Meta:   run.Meta,
				Plan:   run.Plan,
			}
			go func() {
				workerResults <- getStepResult(workerContext, store, commandRun, stepIndex, now, opts)
			}()
		}
		if activeWorkerCount == 0 {
			if ctx.Err() != nil || len(completedSteps) == len(stepOrder) {
				return nil
			}
			return fmt.Errorf("no ready steps remain in the validated dependency graph")
		}
		result := <-workerResults
		activeWorkerCount--
		if result.artifactError != nil {
			return result.artifactError
		}
		if err := addStepResult(ctx, store, run, result, appender, opts); err != nil {
			return err
		}
		completedSteps[run.Plan.Steps[result.stepIndex].ID] = struct{}{}
	}
	return nil
}

func getStepResult(runContext context.Context, store persistence.Store, run RunRecord, stepIndex int, now func() time.Time, opts ExecuteOptions) stepResult {
	step := run.Plan.Steps[stepIndex]
	commandContext := runContext
	if step.Timeout != "" {
		duration, _ := time.ParseDuration(step.Timeout) // The plan is validated before launching workers.
		var cancelCommand context.CancelFunc
		commandContext, cancelCommand = context.WithTimeout(runContext, duration)
		defer cancelCommand()
	}
	runError, exitCode, message := runProcess(commandContext, opts.ForceStop, store, run, stepIndex, step, opts.Stdout, opts.Stderr)
	if errors.Is(runError, errStepLogWrite) {
		return stepResult{stepIndex: stepIndex, artifactError: runError}
	}
	if runError == nil {
		if _, outputError := persistence.ReadEnvFile(store.StepFile(run.RunID, stepIndex, step.ID, persistence.OutputEnv)); outputError != nil {
			runError = outputError
			message = outputError.Error()
		}
	}
	state, timedOut := classifyStepState(runContext, commandContext, runError)
	switch {
	case state == StepStateInterrupted:
		exitCode = nil
		message = getInterruptionMessage(runContext)
	case timedOut:
		exitCode = nil
		message = "timeout after " + step.Timeout
	}
	return stepResult{
		stepIndex:  stepIndex,
		state:      state,
		exitCode:   exitCode,
		message:    message,
		finishedAt: now(),
	}
}

func addUnexecutedStep(ctx context.Context, store persistence.Store, run *RunRecord, stepIndex int, state StepState, message string, at time.Time, appender *events.Appender, opts ExecuteOptions) error {
	status := &run.StepStatuses[stepIndex]
	setTerminalState(status, state, at, nil)
	status.Message = message
	if err := persistStepStatus(store, run.RunID, stepIndex, *status); err != nil {
		return err
	}
	if err := persistSummary(store, run, nil); err != nil {
		return err
	}
	eventType := events.StepBlocked
	if state == StepStateSkipped {
		eventType = events.StepSkipped
	}
	if err := appender.Append(at, eventType, status.StepID, string(state), message); err != nil {
		return err
	}
	printProgress(opts.Progress, "%s %s (%s)\n", state, status.StepID, message)
	contextStatus := getContextStepStatus(run.StepStatuses, *status)
	postError := postStepStatusDuringRun(ctx, opts.Reporter, appender, run.Meta, contextStatus, at, opts.finalReportContext)
	return tolerateGitHubPostFailure(store, run, postError)
}

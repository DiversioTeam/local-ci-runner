package engine

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/DiversioTeam/local-ci-runner/internal/events"
	ghstatus "github.com/DiversioTeam/local-ci-runner/internal/github"
	"github.com/DiversioTeam/local-ci-runner/internal/persistence"
	"github.com/DiversioTeam/local-ci-runner/internal/resultcache"
)

// ReuseOptions configures posting a reused result.
type ReuseOptions struct {
	Reporter ghstatus.Reporter
	Progress io.Writer
	Now      func() time.Time
}

// CheckReusable explains why source cannot stand in for run, or returns nil.
//
// A result is reusable only when it proves the same thing the new run would:
// every step passed, in a snapshot that held exactly the tree run would test,
// with the same configuration and plan.
func CheckReusable(source RunRecord, run RunRecord) error {
	sourceMeta, meta := source.Meta, run.Meta
	configLocation := resultcache.GetConfigLocation(meta.RepoRoot, meta.ConfigPath)
	switch {
	case sourceMeta.FinishedAt == nil || sourceMeta.Interrupted:
		return fmt.Errorf("run %s did not finish", source.RunID)
	case source.Summary.Status != string(StepStateSuccess):
		return fmt.Errorf("run %s finished %s, not success", source.RunID, source.Summary.Status)
	case sourceMeta.SnapshotError != "":
		return fmt.Errorf("run %s: %s", source.RunID, sourceMeta.SnapshotError)
	case sourceMeta.ReusedFrom != nil:
		return fmt.Errorf("run %s reused another result; reuse the original run", source.RunID)
	case sourceMeta.TestedTreeHash == "":
		return fmt.Errorf("run %s did not run in a snapshot", source.RunID)
	case meta.DirtyWorktree || sourceMeta.TestedTreeHash != meta.WorktreeTreeHash:
		return fmt.Errorf("run %s tested tree %s, not %s", source.RunID, sourceMeta.TestedTreeHash, meta.WorktreeTreeHash)
	case sourceMeta.RepoSlug != meta.RepoSlug:
		return fmt.Errorf("run %s belongs to %s", source.RunID, sourceMeta.RepoSlug)
	case configLocation == "" || configLocation != resultcache.GetConfigLocation(sourceMeta.RepoRoot, sourceMeta.ConfigPath):
		return fmt.Errorf("run %s used another or external configuration location", source.RunID)
	case sourceMeta.ConfigHash != meta.ConfigHash:
		return fmt.Errorf("run %s used another configuration", source.RunID)
	case meta.SeedHash == "" || sourceMeta.TestedSeedHash != meta.SeedHash:
		return fmt.Errorf("run %s did not verify the current seeded inputs", source.RunID)
	case sourceMeta.PlanHash != meta.PlanHash:
		return fmt.Errorf("run %s used another plan", source.RunID)
	case len(source.StepStatuses) != len(run.StepStatuses):
		return fmt.Errorf("run %s has a different number of steps", source.RunID)
	}
	// Missing source artifacts invalidate the cache entry before reuse starts, not the new run.
	for stepIndex, status := range source.StepStatuses {
		if _, err := os.ReadFile(storeStepFile(source.RunDir, stepIndex, status.StepID, persistence.OutputEnv)); err != nil {
			return fmt.Errorf("run %s outputs for step %s are unavailable: %w", source.RunID, status.StepID, err)
		}
	}
	return nil
}

// ReuseRun completes a prepared run from source's result without executing a
// step: it records each step's outcome, marks the run reused_from source, and
// posts every context and the aggregate to run's commit with a description
// that names source.
func ReuseRun(ctx context.Context, store persistence.Store, run RunRecord, source RunRecord, opts ReuseOptions) (RunRecord, error) {
	if err := validateStoredStepStatuses(run.Plan, run.StepStatuses); err != nil {
		return RunRecord{}, err
	}
	if err := CheckReusable(source, run); err != nil {
		return RunRecord{}, err
	}
	if err := validateReporter(run.Meta, opts.Reporter); err != nil {
		return RunRecord{}, err
	}
	appender, err := events.NewAppender(store.RunFile(run.RunID, persistence.EventsFile), run.RunID)
	if err != nil {
		return RunRecord{}, err
	}
	now := resolveNow(opts.Now)
	description := buildReuseDescription(persistence.ReusedFrom{RunID: source.RunID, HeadSHA: source.Meta.HeadSHA})

	startedAt := now()
	run.Meta.StartedAt = &startedAt
	runnerPID := os.Getpid()
	run.Meta.RunnerPID = &runnerPID
	run.Meta.ReusedFrom = &persistence.ReusedFrom{RunID: source.RunID, RunDir: source.RunDir, HeadSHA: source.Meta.HeadSHA}
	if err := persistence.WriteJSONFile(store.RunFile(run.RunID, persistence.MetaFile), run.Meta); err != nil {
		return RunRecord{}, err
	}
	if err := persistSummary(store, &run, nil); err != nil {
		return RunRecord{}, err
	}
	if err := appender.Append(startedAt, events.RunStarted, "", runStatusPending, description); err != nil {
		return RunRecord{}, err
	}
	printProgress(opts.Progress, "run %s %s\n", run.RunID, description)

	for stepIndex := range run.StepStatuses {
		status := &run.StepStatuses[stepIndex]
		sourceStatus := source.StepStatuses[stepIndex]
		note := fmt.Sprintf("%s\nlogs: %s\n", description, storeStepFile(source.RunDir, stepIndex, status.StepID, persistence.CombinedLog))
		if err := resetStepArtifacts(store, run.RunID, stepIndex, status.StepID); err != nil {
			return RunRecord{}, err
		}
		output, err := os.ReadFile(storeStepFile(source.RunDir, stepIndex, status.StepID, persistence.OutputEnv))
		if err != nil {
			return RunRecord{}, fmt.Errorf("read reused step %s outputs: %w", status.StepID, err)
		}
		if err := persistence.WriteTextFile(store.StepFile(run.RunID, stepIndex, status.StepID, persistence.OutputEnv), string(output)); err != nil {
			return RunRecord{}, err
		}
		if err := persistence.WriteTextFile(store.StepFile(run.RunID, stepIndex, status.StepID, persistence.CombinedLog), note); err != nil {
			return RunRecord{}, err
		}
		at := now()
		setTerminalState(status, StepState(sourceStatus.State), at, sourceStatus.ExitCode)
		status.Message = description
		if err := persistStepStatus(store, run.RunID, stepIndex, *status); err != nil {
			return RunRecord{}, err
		}
		eventType := events.StepFinished
		if sourceStatus.State == string(StepStateSkipped) {
			eventType = events.StepSkipped
		}
		if err := appender.Append(at, eventType, status.StepID, status.State, description); err != nil {
			return RunRecord{}, err
		}
	}

	finishedAt := now()
	run.Meta.FinishedAt = &finishedAt
	run.Meta.TestedTreeHash = source.Meta.TestedTreeHash
	run.Meta.TestedSeedHash = source.Meta.TestedSeedHash
	if err := persistence.WriteJSONFile(store.RunFile(run.RunID, persistence.MetaFile), run.Meta); err != nil {
		return RunRecord{}, err
	}
	if err := persistSummary(store, &run, &finishedAt); err != nil {
		return RunRecord{}, err
	}
	if err := appender.Append(finishedAt, events.RunFinished, "", run.Summary.Status, description); err != nil {
		return RunRecord{}, err
	}
	printProgress(opts.Progress, "run %s finished: %s\n", run.RunID, run.Summary.Status)

	postedContexts := make(map[string]struct{}, len(run.StepStatuses))
	for _, status := range run.StepStatuses {
		if _, posted := postedContexts[status.GitHubContext]; posted {
			continue
		}
		postedContexts[status.GitHubContext] = struct{}{}
		contextStatus := getContextStepStatus(run.StepStatuses, status)
		postError := postReusedStatus(ctx, opts.Reporter, &appender, run.Meta, contextStatus.StepID, ghstatus.Status{
			Context:     contextStatus.GitHubContext,
			State:       stepGitHubState(contextStatus.State),
			Description: description,
		})
		if err := tolerateGitHubPostFailure(store, &run, postError); err != nil {
			return RunRecord{}, err
		}
	}
	postError := postReusedStatus(ctx, opts.Reporter, &appender, run.Meta, "", ghstatus.Status{
		Context:     run.Meta.GitHubAggregateContext,
		State:       aggregateGitHubState(run.Summary.Status),
		Description: description,
	})
	if err := tolerateGitHubPostFailure(store, &run, postError); err != nil {
		return RunRecord{}, err
	}
	return run, nil
}

func postReusedStatus(ctx context.Context, reporter ghstatus.Reporter, appender *events.Appender, meta persistence.Meta, stepID string, status ghstatus.Status) error {
	if !githubPostingEnabled(meta, reporter) {
		return nil
	}
	return postGitHubStatus(ctx, reporter, appender, meta, stepID, status)
}

func buildReuseDescription(source persistence.ReusedFrom) string {
	return fmt.Sprintf("reused: run %s on %s (same tree)", source.RunID, shortSHA(source.HeadSHA))
}

func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

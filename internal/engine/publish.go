package engine

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/DiversioTeam/local-ci-runner/internal/events"
	ghstatus "github.com/DiversioTeam/local-ci-runner/internal/github"
	"github.com/DiversioTeam/local-ci-runner/internal/persistence"
)

type PublishOptions struct {
	Context   context.Context
	Now       func() time.Time
	Reporter  ghstatus.Reporter
	TargetSHA string
}

// PublishCompletedRun posts the stored terminal statuses for a completed run to a
// specific commit SHA.
//
// This is for the dirty-worktree flow: run locally without posting, commit the
// exact same snapshot, then publish the already-computed result to GitHub.
func PublishCompletedRun(store persistence.Store, run RunRecord, opts PublishOptions) error {
	if err := validateStoredStepStatuses(run.Plan, run.StepStatuses); err != nil {
		return err
	}
	if err := validateStoredSummary(run.Meta, run.Summary, run.StepStatuses); err != nil {
		return err
	}
	if !run.Meta.GitHubEnabled {
		return fmt.Errorf("GitHub posting was disabled for this run")
	}
	if strings.TrimSpace(run.Meta.GitHubPostingSuppressed) == "" {
		return fmt.Errorf("run %s already posted during execution", run.RunID)
	}
	if strings.TrimSpace(opts.TargetSHA) == "" {
		return fmt.Errorf("target SHA is required")
	}
	if run.Meta.FinishedAt == nil || run.Summary.Status == runStatusPending {
		return fmt.Errorf("run %s has not finished yet", run.RunID)
	}
	meta := run.Meta
	meta.HeadSHA = opts.TargetSHA
	meta.GitHubPostingSuppressed = ""
	if err := validateReporter(meta, opts.Reporter); err != nil {
		return err
	}

	appender, err := events.NewAppender(store.RunFile(run.RunID, persistence.EventsFile), run.RunID)
	if err != nil {
		return err
	}
	ctx := resolveContext(opts.Context)
	now := resolveNow(opts.Now)
	at := now()

	for _, status := range run.StepStatuses {
		if err := postStepTerminalStatus(ctx, opts.Reporter, &appender, meta, status, at); err != nil {
			return err
		}
	}
	return postAggregateStatus(ctx, opts.Reporter, &appender, meta, aggregateGitHubState(run.Summary.Status), at)
}

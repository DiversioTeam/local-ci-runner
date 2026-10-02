package engine

import (
	"context"
	"fmt"
	"strings"

	"github.com/DiversioTeam/local-ci-runner/internal/events"
	ghstatus "github.com/DiversioTeam/local-ci-runner/internal/github"
	"github.com/DiversioTeam/local-ci-runner/internal/persistence"
)

// PublishOptions configures publication of a stored run.
type PublishOptions struct {
	Reporter  ghstatus.Reporter
	TargetSHA string
}

// PublishCompletedRun posts the stored terminal statuses for a completed run to a
// specific commit SHA.
//
// This is for the dirty-worktree flow: run locally without posting, commit the
// exact same snapshot, then publish the already-computed result to GitHub.
func PublishCompletedRun(ctx context.Context, store persistence.Store, run RunRecord, opts PublishOptions) error {
	if err := validateStoredStepStatuses(run.Plan, run.StepStatuses); err != nil {
		return err
	}
	if err := validateStoredSummary(run.Meta, run.Summary, run.StepStatuses); err != nil {
		return err
	}
	if !run.Meta.GitHubEnabled {
		return fmt.Errorf("GitHub posting was disabled for this run")
	}
	if run.Meta.FinishedAt == nil || run.Summary.Status == runStatusPending {
		return fmt.Errorf("run %s has not finished yet", run.RunID)
	}
	if run.Summary.Status == string(StepStateInterrupted) {
		return fmt.Errorf("run %s was interrupted", run.RunID)
	}
	if strings.TrimSpace(run.Meta.GitHubPostingSuppressed) == "" {
		return fmt.Errorf("run %s was configured to post during execution; publish requires a suppressed run (inspect receipts; configuration is not proof of publication)", run.RunID)
	}
	if strings.TrimSpace(opts.TargetSHA) == "" {
		return fmt.Errorf("target SHA is required")
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
	appender.PublicationSource = events.PublicationPublish

	postedContexts := make(map[string]struct{})
	for _, status := range run.StepStatuses {
		if _, posted := postedContexts[status.GitHubContext]; posted {
			continue
		}
		contextStatus := getContextStepStatus(run.StepStatuses, status)
		if err := postStepTerminalStatus(ctx, opts.Reporter, &appender, meta, contextStatus); err != nil {
			return err
		}
		postedContexts[status.GitHubContext] = struct{}{}
	}
	// Publication deliberately leaves run metadata untouched: the run is the
	// evidence of what was verified, and repeat publications are recorded as
	// further attempts in the event log rather than by rewriting that evidence.
	return postAggregateStatus(ctx, opts.Reporter, &appender, meta, aggregateGitHubState(run.Summary.Status))
}

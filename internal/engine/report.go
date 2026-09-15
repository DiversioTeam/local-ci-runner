package engine

import (
	"context"
	"crypto/rand"
	"fmt"
	"strings"
	"time"

	"github.com/DiversioTeam/local-ci-runner/internal/events"
	ghstatus "github.com/DiversioTeam/local-ci-runner/internal/github"
	"github.com/DiversioTeam/local-ci-runner/internal/persistence"
)

// githubStatusPostError separates recoverable remote failures from local event-write failures.
type githubStatusPostError struct {
	cause error
}

func (postError *githubStatusPostError) Error() string {
	return postError.cause.Error()
}

func (postError *githubStatusPostError) Unwrap() error {
	return postError.cause
}

const (
	aggregateDescriptionRunning     = "local verification running"
	aggregateDescriptionPassed      = "local verification passed"
	aggregateDescriptionFailed      = "local verification failed"
	aggregateDescriptionInterrupted = "local verification interrupted"
	stepDescriptionRunning          = "running"
	stepDescriptionPassed           = "passed"
	stepDescriptionFailed           = "failed"
	stepDescriptionInterrupted      = "interrupted"
	stepDescriptionSkipped          = "skipped"
	stepDescriptionBlocked          = "blocked"
)

func validateReporter(meta persistence.Meta, reporter ghstatus.Reporter) error {
	if meta.GitHubEnabled && strings.TrimSpace(meta.GitHubPostingSuppressed) == "" && reporter == nil {
		return fmt.Errorf("GitHub posting is enabled but no reporter was configured")
	}
	if meta.GitHubEnabled && strings.TrimSpace(meta.GitHubAggregateContext) == "" {
		return fmt.Errorf("GitHub aggregate context is required when GitHub posting is enabled")
	}
	return nil
}

func githubPostingEnabled(meta persistence.Meta, reporter ghstatus.Reporter) bool {
	return meta.GitHubEnabled && reporter != nil && strings.TrimSpace(meta.GitHubPostingSuppressed) == ""
}

func postAggregateStatus(
	ctx context.Context,
	reporter ghstatus.Reporter,
	appender *events.Appender,
	meta persistence.Meta,
	state ghstatus.State,
	at time.Time,
) error {
	if !githubPostingEnabled(meta, reporter) {
		return nil
	}

	status := ghstatus.Status{
		Context:     meta.GitHubAggregateContext,
		State:       state,
		Description: aggregateDescription(state),
	}
	return postGitHubStatus(ctx, reporter, appender, meta, "", status, at)
}

func postStepPendingStatus(
	ctx context.Context,
	reporter ghstatus.Reporter,
	appender *events.Appender,
	meta persistence.Meta,
	status persistence.StepStatus,
	at time.Time,
) error {
	if !githubPostingEnabled(meta, reporter) {
		return nil
	}

	githubStatus := ghstatus.Status{
		Context:     status.GitHubContext,
		State:       ghstatus.StatePending,
		Description: stepDescriptionRunning,
	}
	return postGitHubStatus(ctx, reporter, appender, meta, status.StepID, githubStatus, at)
}

func postStepTerminalStatus(
	ctx context.Context,
	reporter ghstatus.Reporter,
	appender *events.Appender,
	meta persistence.Meta,
	status persistence.StepStatus,
	at time.Time,
) error {
	if !githubPostingEnabled(meta, reporter) {
		return nil
	}

	githubStatus := ghstatus.Status{
		Context:     status.GitHubContext,
		State:       stepGitHubState(status.State),
		Description: stepDescription(status.State),
	}
	return postGitHubStatus(ctx, reporter, appender, meta, status.StepID, githubStatus, at)
}

func postGitHubStatus(
	ctx context.Context,
	reporter ghstatus.Reporter,
	appender *events.Appender,
	meta persistence.Meta,
	stepID string,
	status ghstatus.Status,
	_ time.Time,
) error {
	post := events.GitHubPost{Version: events.PublicationVersion, AttemptID: rand.Text(), Repo: meta.RepoSlug, SHA: meta.HeadSHA,
		Context: status.Context, Source: appender.PublicationSource}
	if err := appender.AddGitHubPost(time.Now().UTC(), events.GitHubStatusRequested, stepID, string(status.State), post); err != nil {
		return fmt.Errorf("record publication intent; no request sent: %w", err)
	}
	target := ghstatus.Target{Repo: meta.RepoSlug, SHA: meta.HeadSHA}
	if err := reporter.PostStatus(ctx, target, status); err != nil {
		if appendError := appender.AddGitHubPost(time.Now().UTC(), events.GitHubStatusFailed, stepID, string(status.State), post); appendError != nil {
			return fmt.Errorf("record GitHub status failure: %w", appendError)
		}
		return &githubStatusPostError{cause: fmt.Errorf("post GitHub status %s: %w", githubEventMessage(status), err)}
	}
	if err := appender.AddGitHubPost(time.Now().UTC(), events.GitHubStatusPosted, stepID, string(status.State), post); err != nil {
		return fmt.Errorf("GitHub accepted status but receipt was not saved; inspect before retrying: %w", err)
	}
	return nil
}

func aggregateGitHubState(runStatus string) ghstatus.State {
	switch runStatus {
	case string(StepStateSuccess), string(StepStateSkipped):
		return ghstatus.StateSuccess
	case runStatusPending, string(StepStateRunning):
		return ghstatus.StatePending
	case string(StepStateInterrupted):
		return ghstatus.StateError
	default:
		return ghstatus.StateFailure
	}
}

func aggregateDescription(state ghstatus.State) string {
	switch state {
	case ghstatus.StateSuccess:
		return aggregateDescriptionPassed
	case ghstatus.StatePending:
		return aggregateDescriptionRunning
	case ghstatus.StateError:
		return aggregateDescriptionInterrupted
	default:
		return aggregateDescriptionFailed
	}
}

func stepGitHubState(stepState string) ghstatus.State {
	switch stepState {
	case string(StepStateSuccess), string(StepStateSkipped):
		return ghstatus.StateSuccess
	case string(StepStatePending), string(StepStateRunning), string(StepStateStale):
		return ghstatus.StatePending
	case string(StepStateInterrupted):
		return ghstatus.StateError
	default:
		return ghstatus.StateFailure
	}
}

func stepDescription(stepState string) string {
	switch stepState {
	case string(StepStateSuccess):
		return stepDescriptionPassed
	case string(StepStateSkipped):
		return stepDescriptionSkipped
	case string(StepStateBlocked):
		return stepDescriptionBlocked
	case string(StepStateInterrupted):
		return stepDescriptionInterrupted
	case string(StepStatePending), string(StepStateRunning), string(StepStateStale):
		return stepDescriptionRunning
	default:
		return stepDescriptionFailed
	}
}

func githubEventMessage(status ghstatus.Status) string {
	return status.Context + "=" + string(status.State)
}

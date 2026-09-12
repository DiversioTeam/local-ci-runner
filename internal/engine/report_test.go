package engine

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DiversioTeam/local-ci-runner/internal/config"
	"github.com/DiversioTeam/local-ci-runner/internal/events"
	ghstatus "github.com/DiversioTeam/local-ci-runner/internal/github"
	"github.com/DiversioTeam/local-ci-runner/internal/persistence"
)

type recordedStatus struct {
	target ghstatus.Target
	status ghstatus.Status
}

type fakeReporter struct {
	posts         []recordedStatus
	contextErrors []error
	err           error
}

func (reporter *fakeReporter) PostStatus(reportContext context.Context, target ghstatus.Target, status ghstatus.Status) error {
	if reporter.err != nil {
		return reporter.err
	}
	reporter.contextErrors = append(reporter.contextErrors, reportContext.Err())
	reporter.posts = append(reporter.posts, recordedStatus{target: target, status: status})
	return nil
}

func TestValidateReporterRequiresReporterWhenEnabled(t *testing.T) {
	t.Parallel()

	err := validateReporter(persistence.Meta{GitHubEnabled: true, GitHubAggregateContext: "local/verify"}, nil)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestValidateReporterAllowsSuppressedGitHubPosting(t *testing.T) {
	t.Parallel()

	err := validateReporter(persistence.Meta{GitHubEnabled: true, GitHubAggregateContext: "local/verify", GitHubPostingSuppressed: "dirty_worktree"}, nil)
	if err != nil {
		t.Fatalf("validateReporter() error = %v", err)
	}
}

func TestAggregateGitHubState(t *testing.T) {
	t.Parallel()

	cases := []struct {
		runStatus string
		want      ghstatus.State
	}{
		{runStatus: string(StepStateSuccess), want: ghstatus.StateSuccess},
		{runStatus: string(StepStateSkipped), want: ghstatus.StateSuccess},
		{runStatus: runStatusPending, want: ghstatus.StatePending},
		{runStatus: string(StepStateFailure), want: ghstatus.StateFailure},
		{runStatus: string(StepStateInterrupted), want: ghstatus.StateError},
		{runStatus: string(StepStateBlocked), want: ghstatus.StateFailure},
	}
	for _, testCase := range cases {
		t.Run(testCase.runStatus, func(t *testing.T) {
			if got := aggregateGitHubState(testCase.runStatus); got != testCase.want {
				t.Fatalf("aggregateGitHubState(%q) = %q, want %q", testCase.runStatus, got, testCase.want)
			}
		})
	}
}

func TestPostGitHubStatusAppendsEvent(t *testing.T) {
	t.Parallel()

	path := writeEventFile(t)
	appender, err := events.NewAppender(path, "run-1")
	if err != nil {
		t.Fatalf("NewAppender() error = %v", err)
	}
	reporter := &fakeReporter{}
	meta := persistence.Meta{RepoSlug: "owner/repo", HeadSHA: "abc123", GitHubEnabled: true, GitHubAggregateContext: "local/verify"}
	at := time.Date(2026, 6, 27, 15, 4, 5, 0, time.UTC)

	err = postAggregateStatus(t.Context(), reporter, &appender, meta, ghstatus.StatePending, at)
	if err != nil {
		t.Fatalf("postAggregateStatus() error = %v", err)
	}
	if len(reporter.posts) != 1 {
		t.Fatalf("post count = %d, want 1", len(reporter.posts))
	}
	if got, want := reporter.posts[0].status.Context, "local/verify"; got != want {
		t.Fatalf("context = %q, want %q", got, want)
	}
	items, err := events.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].Type != events.GitHubStatusRequested || items[1].Type != events.GitHubStatusPosted {
		t.Fatalf("expected request then acknowledgement, got %#v", items)
	}
	if *items[0].GitHubPost != *items[1].GitHubPost || items[1].GitHubPost.SHA != meta.HeadSHA || items[1].Time.Before(items[0].Time) {
		t.Fatalf("incorrect request identity or acknowledgement time: %#v", items)
	}
}

func TestPublishCompletedRunPostsTerminalStatuses(t *testing.T) {
	t.Parallel()

	plan := config.ResolvedPlan{Steps: []config.Step{
		{ID: "lint", Command: []string{"/bin/sh", "-c", "printf 'ok\\n'"}},
		{ID: "test", Command: []string{"/bin/sh", "-c", "printf 'ok\\n'"}, Needs: []string{"lint"}},
	}}
	plan.ApplyDefaults()
	fixture := newRunFixtureWithGitHub(t, plan, config.GitHub{Enabled: true})
	run, err := PrepareRun(fixture.store, PrepareOptions{
		Identity:                fixture.identity,
		Plan:                    fixture.plan,
		GitHub:                  fixture.github,
		HeadTreeHash:            "head-tree",
		WorktreeTreeHash:        "worktree-tree",
		DirtyWorktree:           true,
		GitHubPostingSuppressed: "dirty_worktree",
		Now:                     fixedRunTime,
		Random:                  bytes.NewReader(cloneBytes(fixedRunEntropy)),
	})
	if err != nil {
		t.Fatalf("PrepareRun() error = %v", err)
	}
	completed, err := ExecuteRun(t.Context(), fixture.store, run, ExecuteOptions{})
	if err != nil {
		t.Fatalf("ExecuteRun() error = %v", err)
	}

	reporter := &fakeReporter{}
	err = PublishCompletedRun(t.Context(), fixture.store, completed, PublishOptions{
		Reporter:  reporter,
		TargetSHA: "def456",
		Now:       func() time.Time { return fixedRunTime },
	})
	if err != nil {
		t.Fatalf("PublishCompletedRun() error = %v", err)
	}
	if len(reporter.posts) != 3 {
		t.Fatalf("post count = %d, want 3", len(reporter.posts))
	}
	if got, want := reporter.posts[0].target.SHA, "def456"; got != want {
		t.Fatalf("target SHA = %q, want %q", got, want)
	}
}

func TestPublishCompletedRunRejectsAlreadyPostedRun(t *testing.T) {
	t.Parallel()

	plan := config.ResolvedPlan{Steps: []config.Step{{ID: "lint", Command: []string{"/bin/sh", "-c", "printf 'ok\\n'"}}}}
	plan.ApplyDefaults()
	fixture := newRunFixtureWithGitHub(t, plan, config.GitHub{Enabled: true})
	run, err := PrepareRun(fixture.store, PrepareOptions{
		Identity:         fixture.identity,
		Plan:             fixture.plan,
		GitHub:           fixture.github,
		HeadTreeHash:     "head-tree",
		WorktreeTreeHash: "head-tree",
		Now:              fixedRunTime,
		Random:           bytes.NewReader(cloneBytes(fixedRunEntropy)),
	})
	if err != nil {
		t.Fatalf("PrepareRun() error = %v", err)
	}
	completed, err := ExecuteRun(t.Context(), fixture.store, run, ExecuteOptions{Reporter: &fakeReporter{}})
	if err != nil {
		t.Fatalf("ExecuteRun() error = %v", err)
	}

	err = PublishCompletedRun(t.Context(), fixture.store, completed, PublishOptions{
		Reporter:  &fakeReporter{},
		TargetSHA: "def456",
		Now:       func() time.Time { return fixedRunTime },
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if got, want := err.Error(), "publish requires a suppressed run"; !strings.Contains(got, want) {
		t.Fatalf("error = %v, want substring %q", err, want)
	}
}

func TestPostGitHubStatusReturnsReporterError(t *testing.T) {
	t.Parallel()

	path := writeEventFile(t)
	appender, err := events.NewAppender(path, "run-1")
	if err != nil {
		t.Fatalf("NewAppender() error = %v", err)
	}
	reporter := &fakeReporter{err: fmt.Errorf("boom")}
	meta := persistence.Meta{RepoSlug: "owner/repo", HeadSHA: "abc123", GitHubEnabled: true, GitHubAggregateContext: "local/verify"}

	err = postAggregateStatus(t.Context(), reporter, &appender, meta, ghstatus.StatePending, fixedRunTime)
	if err == nil {
		t.Fatal("expected error")
	}
	payload := mustReadEventLog(t, path)
	if payload == "" {
		t.Fatal("expected failed event")
	}
}

func writeEventFile(t *testing.T) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), persistence.EventsFile)
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", path, err)
	}
	return path
}

func mustReadEventLog(t *testing.T, path string) string {
	t.Helper()

	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s) error = %v", path, err)
	}
	return string(payload)
}

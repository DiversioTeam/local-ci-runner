package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DiversioTeam/local-ci-runner/internal/config"
	"github.com/DiversioTeam/local-ci-runner/internal/events"
	ghstatus "github.com/DiversioTeam/local-ci-runner/internal/github"
	"github.com/DiversioTeam/local-ci-runner/internal/persistence"
)

type recordedStatus struct {
	target ghstatus.Target
	status ghstatus.Status
}

// fakeReporter records posts without locking: only the scheduler goroutine reports, and -race checks that.
type fakeReporter struct {
	posts          []recordedStatus
	contextErrors  []error
	postError      error
	failureAttempt int
	attempts       int
}

func (reporter *fakeReporter) PostStatus(reportContext context.Context, target ghstatus.Target, status ghstatus.Status) error {
	reporter.attempts++
	if reporter.postError != nil && (reporter.failureAttempt == 0 || reporter.attempts == reporter.failureAttempt) {
		return reporter.postError
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

func TestPublishCompletedRunRefusesIneligibleRuns(t *testing.T) {
	t.Parallel()

	plan := config.ResolvedPlan{Steps: []config.Step{{ID: "lint", Command: []string{"/bin/sh", "-c", "exit 0"}}}}
	plan.ApplyDefaults()
	for _, test := range []struct {
		name      string
		github    config.GitHub
		prepare   func(t *testing.T, fixture runFixture) RunRecord
		wantError string
	}{
		{
			name:   "posted during execution",
			github: config.GitHub{Enabled: true},
			prepare: func(t *testing.T, fixture runFixture) RunRecord {
				completed, err := ExecuteRun(t.Context(), fixture.store, prepareRunFixture(t, fixture), ExecuteOptions{Reporter: &fakeReporter{}})
				if err != nil {
					t.Fatal(err)
				}
				return completed
			},
			wantError: "configured to post during execution",
		},
		{
			name:   "not finished",
			github: config.GitHub{Enabled: true},
			prepare: func(t *testing.T, fixture runFixture) RunRecord {
				return prepareSuppressedRunFixture(t, fixture, "cli_disabled")
			},
			wantError: "has not finished",
		},
		{
			name:   "interrupted",
			github: config.GitHub{Enabled: true},
			prepare: func(t *testing.T, fixture runFixture) RunRecord {
				canceled, cancel := context.WithCancel(t.Context())
				cancel()
				interrupted, err := ExecuteRun(canceled, fixture.store, prepareSuppressedRunFixture(t, fixture, "cli_disabled"), ExecuteOptions{})
				if err != nil {
					t.Fatal(err)
				}
				return interrupted
			},
			wantError: "was interrupted",
		},
		{
			name: "GitHub disabled",
			prepare: func(t *testing.T, fixture runFixture) RunRecord {
				completed, err := ExecuteRun(t.Context(), fixture.store, prepareRunFixture(t, fixture), ExecuteOptions{})
				if err != nil {
					t.Fatal(err)
				}
				return completed
			},
			wantError: "GitHub posting was disabled",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fixture := newRunFixtureWithGitHub(t, plan, test.github)
			run := test.prepare(t, fixture)
			err := PublishCompletedRun(t.Context(), fixture.store, run, PublishOptions{Reporter: &fakeReporter{}, TargetSHA: "def456"})
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("error = %v, want %q", err, test.wantError)
			}
		})
	}
}

func TestPostGitHubStatusReturnsReporterError(t *testing.T) {
	t.Parallel()

	path := writeEventFile(t)
	appender, err := events.NewAppender(path, "run-1")
	if err != nil {
		t.Fatalf("NewAppender() error = %v", err)
	}
	reporter := &fakeReporter{postError: errors.New("boom")}
	meta := persistence.Meta{RepoSlug: "owner/repo", HeadSHA: "abc123", GitHubEnabled: true, GitHubAggregateContext: "local/verify"}

	err = postAggregateStatus(t.Context(), reporter, &appender, meta, ghstatus.StatePending)
	var postError *githubStatusPostError
	if !errors.As(err, &postError) || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("postAggregateStatus() error = %v, want a githubStatusPostError keeping its cause", err)
	}
	items, err := events.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].Type != events.GitHubStatusRequested || items[1].Type != events.GitHubStatusFailed || *items[0].GitHubPost != *items[1].GitHubPost {
		t.Fatalf("want a request then its failure receipt, got %#v", items)
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

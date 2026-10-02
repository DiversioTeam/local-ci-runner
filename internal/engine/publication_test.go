package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/DiversioTeam/local-ci-runner/internal/config"
	"github.com/DiversioTeam/local-ci-runner/internal/events"
	ghstatus "github.com/DiversioTeam/local-ci-runner/internal/github"
	"github.com/DiversioTeam/local-ci-runner/internal/persistence"
)

type publicationReporter func(ghstatus.Target, ghstatus.Status) error

func (reporter publicationReporter) PostStatus(_ context.Context, target ghstatus.Target, status ghstatus.Status) error {
	return reporter(target, status)
}

// buildPublicationFixture executes a two-step run (one pass, one fail) and returns the
// targets the reporter received during execution.
func buildPublicationFixture(t *testing.T, suppressed bool) (persistence.Store, RunRecord, []ghstatus.Target) {
	t.Helper()
	plan := config.ResolvedPlan{Steps: []config.Step{
		{ID: "pass", Command: []string{"/bin/sh", "-c", "exit 0"}},
		{ID: "fail", Command: []string{"/bin/sh", "-c", "exit 1"}},
	}}
	plan.ApplyDefaults()
	fixture := newRunFixtureWithGitHub(t, plan, config.GitHub{Enabled: true})
	reason := ""
	if suppressed {
		reason = "cli_disabled"
	}
	run := prepareSuppressedRunFixture(t, fixture, reason)
	var targets []ghstatus.Target
	reporter := publicationReporter(func(target ghstatus.Target, _ ghstatus.Status) error {
		targets = append(targets, target)
		items, err := events.ReadFile(fixture.store.RunFile(run.RunID, persistence.EventsFile))
		if err != nil {
			t.Fatal(err)
		}
		if items[len(items)-1].Type != events.GitHubStatusRequested {
			t.Fatal("network call before intent")
		}
		return nil
	})
	completed, err := ExecuteRun(t.Context(), fixture.store, run, ExecuteOptions{Reporter: reporter})
	if err != nil {
		t.Fatal(err)
	}
	return fixture.store, completed, targets
}

func TestPublicationEventsPreserveTargetsAndRetryHistory(t *testing.T) {
	t.Run("execution", func(t *testing.T) {
		store, run, targets := buildPublicationFixture(t, false)
		if len(targets) != 6 {
			t.Fatalf("execution posts = %d, want 6", len(targets))
		}
		for _, target := range targets {
			if target.SHA != run.Meta.HeadSHA {
				t.Fatalf("execution posted to %s, want HEAD %s", target.SHA, run.Meta.HeadSHA)
			}
		}
		requests, acknowledgements, failures := validatePublicationReceipts(t, store, run, events.PublicationExecution, run.Meta.HeadSHA)
		if requests != 6 || acknowledgements != 6 || failures != 0 {
			t.Fatalf("requests=%d ack=%d errors=%d", requests, acknowledgements, failures)
		}
	})
	t.Run("publish", func(t *testing.T) {
		store, run, _ := buildPublicationFixture(t, true)
		metaPath := store.RunFile(run.RunID, persistence.MetaFile)
		before := mustReadFile(t, metaPath)
		calls := 0
		lostResponse := publicationReporter(func(_ ghstatus.Target, _ ghstatus.Status) error {
			calls++
			if calls == 2 {
				return errors.New("response lost")
			}
			return nil
		})
		if err := PublishCompletedRun(t.Context(), store, run, PublishOptions{Reporter: lostResponse, TargetSHA: "target-one"}); err == nil {
			t.Fatal("expected reporting error")
		}
		// Retries are new attempts: each posts both step contexts and the aggregate to its target.
		for _, targetSHA := range []string{"target-one", "target-two"} {
			reporter := &fakeReporter{}
			if err := PublishCompletedRun(t.Context(), store, run, PublishOptions{Reporter: reporter, TargetSHA: targetSHA}); err != nil {
				t.Fatal(err)
			}
			if len(reporter.posts) != 3 {
				t.Fatalf("publication posts = %d, want 3", len(reporter.posts))
			}
			for _, post := range reporter.posts {
				if post.target.SHA != targetSHA {
					t.Fatalf("published to %s, want %s", post.target.SHA, targetSHA)
				}
			}
		}
		if mustReadFile(t, metaPath) != before {
			t.Fatal("publication changed run metadata")
		}
		requests, acknowledgements, failures := validatePublicationReceipts(t, store, run, events.PublicationPublish, "target-one", "target-two")
		if requests != 8 || acknowledgements != 7 || failures != 1 {
			t.Fatalf("requests=%d ack=%d errors=%d", requests, acknowledgements, failures)
		}
	})
}

// validatePublicationReceipts checks that every outcome pairs with an earlier identical request
// from source to one of the SHAs, and returns how many requests, acknowledgements and failures exist.
func validatePublicationReceipts(t *testing.T, store persistence.Store, run RunRecord, source events.PublicationSource, shas ...string) (requests, acknowledgements, failures int) {
	t.Helper()
	items, err := events.ReadFile(store.RunFile(run.RunID, persistence.EventsFile))
	if err != nil {
		t.Fatal(err)
	}
	requested := map[string]events.Event{}
	for _, item := range items {
		if item.GitHubPost == nil {
			continue
		}
		post := *item.GitHubPost
		if post.Version != events.PublicationVersion || post.Repo != run.Meta.RepoSlug || post.Context == "" || post.Source != source || !slices.Contains(shas, post.SHA) {
			t.Fatalf("bad receipt target: %+v", post)
		}
		if item.Type == events.GitHubStatusRequested {
			if _, exists := requested[post.AttemptID]; exists {
				t.Fatal("reused request ID")
			}
			requested[post.AttemptID] = item
			continue
		}
		request, exists := requested[post.AttemptID]
		if !exists || *request.GitHubPost != post || request.Status != item.Status || item.Time.Before(request.Time) {
			t.Fatal("outcome does not match request")
		}
		switch item.Type {
		case events.GitHubStatusPosted:
			acknowledgements++
		case events.GitHubStatusFailed:
			failures++
		}
	}
	return len(requested), acknowledgements, failures
}

func TestPublicationPersistenceFailuresDoNotInventAcknowledgements(t *testing.T) {
	for _, test := range []struct {
		name string
		// blockBefore stops event writes before the request; otherwise the reporter stops them.
		blockBefore bool
		postError   error
		wantError   string
		wantCalls   int
	}{
		{name: "intent cannot be saved", blockBefore: true, wantError: "no request sent", wantCalls: 0},
		{name: "acknowledgement cannot be saved", wantError: "accepted status", wantCalls: 1},
		{name: "failure receipt cannot be saved", postError: errors.New("boom"), wantError: "record GitHub status failure", wantCalls: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, run, _ := buildPublicationFixture(t, true)
			path := store.RunFile(run.RunID, persistence.EventsFile)
			appender, err := events.NewAppender(path, run.RunID)
			if err != nil {
				t.Fatal(err)
			}
			saved := filepath.Join(t.TempDir(), "events.jsonl")
			blockWrites := func() {
				if err := os.Rename(path, saved); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			calls := 0
			reporter := publicationReporter(func(_ ghstatus.Target, _ ghstatus.Status) error {
				calls++
				blockWrites()
				return test.postError
			})
			if test.blockBefore {
				blockWrites()
			}
			meta := run.Meta
			meta.GitHubPostingSuppressed = ""
			err = postAggregateStatus(t.Context(), reporter, &appender, meta, ghstatus.StateSuccess)
			// A lost local record is fatal, never a recoverable GitHub posting failure.
			var postError *githubStatusPostError
			if err == nil || errors.As(err, &postError) || !strings.Contains(err.Error(), test.wantError) || calls != test.wantCalls {
				t.Fatalf("error = %v, calls = %d; want %q after %d calls", err, calls, test.wantError, test.wantCalls)
			}
			if calls > 0 {
				items, err := events.ReadFile(saved)
				if err != nil {
					t.Fatal(err)
				}
				if items[len(items)-1].Type != events.GitHubStatusRequested {
					t.Fatal("invented an outcome receipt that was never saved")
				}
			}
		})
	}
}

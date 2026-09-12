package engine

import (
	"bytes"
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

type publicationReporter func(ghstatus.Target, ghstatus.Status) error

func (reporter publicationReporter) PostStatus(_ context.Context, target ghstatus.Target, status ghstatus.Status) error {
	return reporter(target, status)
}

func buildPublicationFixture(t *testing.T, suppressed bool) (persistence.Store, RunRecord) {
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
	run, err := PrepareRun(fixture.store, PrepareOptions{Identity: fixture.identity, Plan: fixture.plan, GitHub: fixture.github,
		GitHubPostingSuppressed: reason, Now: fixedRunTime, Random: bytes.NewReader(cloneBytes(fixedRunEntropy))})
	if err != nil {
		t.Fatal(err)
	}
	reporter := publicationReporter(func(_ ghstatus.Target, _ ghstatus.Status) error {
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
	return fixture.store, completed
}

func TestPublicationEventsPreserveTargetsAndRetryHistory(t *testing.T) {
	for _, suppressed := range []bool{false, true} {
		store, run := buildPublicationFixture(t, suppressed)
		metaPath := store.RunFile(run.RunID, persistence.MetaFile)
		before, err := os.ReadFile(metaPath)
		if err != nil {
			t.Fatal(err)
		}
		if suppressed {
			calls := 0
			reporter := publicationReporter(func(_ ghstatus.Target, _ ghstatus.Status) error {
				calls++
				if calls == 2 {
					return errors.New("response lost")
				}
				return nil
			})
			if err := PublishCompletedRun(t.Context(), store, run, PublishOptions{Reporter: reporter, TargetSHA: "target-one"}); err == nil {
				t.Fatal("expected reporting error")
			}
			for _, target := range []string{"target-one", "target-two"} {
				if err := PublishCompletedRun(t.Context(), store, run, PublishOptions{Reporter: &fakeReporter{}, TargetSHA: target}); err != nil {
					t.Fatal(err)
				}
			}
		}
		after, err := os.ReadFile(metaPath)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(before, after) {
			t.Fatal("publication changed run metadata")
		}
		items, err := events.ReadFile(store.RunFile(run.RunID, persistence.EventsFile))
		if err != nil {
			t.Fatal(err)
		}
		requests := map[string]events.Event{}
		acknowledgements, failures := 0, 0
		for _, item := range items {
			if item.GitHubPost == nil {
				continue
			}
			post := *item.GitHubPost
			if post.Version != events.PublicationVersion || post.Repo != run.Meta.RepoSlug || post.Context == "" {
				t.Fatalf("bad target: %+v", post)
			}
			if suppressed {
				if post.Source != events.PublicationPublish || (post.SHA != "target-one" && post.SHA != "target-two") {
					t.Fatal(post)
				}
			} else if post.Source != events.PublicationExecution || post.SHA != run.Meta.HeadSHA {
				t.Fatal(post)
			}
			if item.Type == events.GitHubStatusRequested {
				if _, exists := requests[post.AttemptID]; exists {
					t.Fatal("reused request ID")
				}
				requests[post.AttemptID] = item
				continue
			}
			request, exists := requests[post.AttemptID]
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
		if suppressed && (len(requests) != 8 || acknowledgements != 7 || failures != 1) {
			t.Fatalf("requests=%d ack=%d errors=%d", len(requests), acknowledgements, failures)
		}
		if !suppressed && (len(requests) != 6 || acknowledgements != 6) {
			t.Fatal("missing automatic posting events")
		}
	}
}

func TestPublicationPersistenceFailuresDoNotInventAcknowledgements(t *testing.T) {
	for _, beforeRequest := range []bool{true, false} {
		store, run := buildPublicationFixture(t, true)
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
			if err := os.Mkdir(path, 0700); err != nil {
				t.Fatal(err)
			}
		}
		calls := 0
		reporter := publicationReporter(func(_ ghstatus.Target, _ ghstatus.Status) error { calls++; blockWrites(); return nil })
		if beforeRequest {
			blockWrites()
		}
		meta := run.Meta
		meta.GitHubPostingSuppressed = ""
		err = postAggregateStatus(t.Context(), reporter, &appender, meta, ghstatus.StateSuccess, fixedRunTime)
		if err == nil {
			t.Fatal("expected persistence error")
		}
		if beforeRequest && calls != 0 {
			t.Fatal("posted without durable intent")
		}
		if !beforeRequest {
			if calls != 1 || !strings.Contains(err.Error(), "accepted status") {
				t.Fatal(err)
			}
			items, err := events.ReadFile(saved)
			if err != nil {
				t.Fatal(err)
			}
			if items[len(items)-1].Type != events.GitHubStatusRequested {
				t.Fatal("invented acknowledgement")
			}
		}
	}
}

func TestPublicationWriterRefusesTornTail(t *testing.T) {
	path := writeEventFile(t)
	if err := os.WriteFile(path, []byte(`{"github_post":`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := events.ReadFile(path); err != nil {
		t.Fatal("inspection must tolerate a torn tail", err)
	}
	if _, err := events.NewAppender(path, "run-1"); err == nil {
		t.Fatal("writer accepted torn event log")
	}
}

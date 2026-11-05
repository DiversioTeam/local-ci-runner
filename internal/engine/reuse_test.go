package engine

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DiversioTeam/local-ci-runner/internal/config"
	ghstatus "github.com/DiversioTeam/local-ci-runner/internal/github"
	"github.com/DiversioTeam/local-ci-runner/internal/persistence"
)

// newReusePair runs a plan once in a snapshot (the source) and prepares a second run of the same
// tree, config, and plan in the same store (the candidate).
func newReusePair(t *testing.T, plan config.ResolvedPlan, githubConfig config.GitHub, suppressed string) (persistence.Store, RunRecord, RunRecord) {
	t.Helper()
	_, store, run := newSnapshotRun(t, plan, config.GitHub{}, "")
	source, err := ExecuteRun(t.Context(), store, run, ExecuteOptions{Snapshot: &SnapshotOptions{}})
	if err != nil {
		t.Fatal(err)
	}
	identity := RunIdentity{RepoRoot: run.Meta.RepoRoot, RepoSlug: run.Meta.RepoSlug, HeadSHA: "5555555555555555555555555555555555555555", ConfigPath: run.Meta.ConfigPath, ConfigHash: run.Meta.ConfigHash, PlanHash: run.Meta.PlanHash, WorktreeTreeHash: run.Meta.WorktreeTreeHash}
	if githubConfig.Enabled && githubConfig.AggregateContext == "" {
		githubConfig.AggregateContext = config.DefaultAggregateContext
	}
	candidate, err := PrepareRun(store, PrepareOptions{
		Identity:                identity,
		Plan:                    source.Plan,
		GitHub:                  githubConfig,
		GitHubPostingSuppressed: suppressed,
		HeadTreeHash:            run.Meta.WorktreeTreeHash,
		WorktreeTreeHash:        run.Meta.WorktreeTreeHash,
		Random:                  bytes.NewReader([]byte{0xca, 0xfe, 0xba, 0xbe}),
	})
	if err != nil {
		t.Fatal(err)
	}
	return store, source, candidate
}

func TestReusePreservesProducerOutputsForDownstreamResume(t *testing.T) {
	t.Parallel()
	plan := config.ResolvedPlan{Steps: []config.Step{
		{ID: "produce", Command: []string{"/bin/sh", "-c", `printf 'VALUE=ready\n' > "$LOCAL_CI_STEP_OUTPUT"`}},
		{ID: "consume", Needs: []string{"produce"}, Command: []string{"/bin/sh", "-c", `. "$LOCAL_CI_RUN_DIR/steps/001-produce/output.env"; test "$VALUE" = ready`}},
	}}
	store, source, candidate := newReusePair(t, plan, config.GitHub{}, "")
	reused, err := ReuseRun(t.Context(), store, candidate, source, ReuseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadRun(store, reused.RunID)
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := ExecuteRun(t.Context(), store, loaded, ExecuteOptions{FromStep: "consume"})
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Summary.Status != string(StepStateSuccess) {
		t.Fatalf("downstream resume lost the producer output: %s", resumed.Summary.Status)
	}
}

func TestPublishReusedRunPreservesSourceDescriptions(t *testing.T) {
	t.Parallel()
	for _, suppression := range []string{"cli_disabled", "post_failed"} {
		t.Run(suppression, func(t *testing.T) {
			t.Parallel()
			suppressed := suppression
			reporter := &fakeReporter{}
			if suppression == "post_failed" {
				suppressed = ""
				reporter.postError = errors.New("offline")
			}
			plan := config.ResolvedPlan{Steps: []config.Step{{ID: "check", Command: []string{"true"}}}}
			store, source, candidate := newReusePair(t, plan, config.GitHub{Enabled: true}, suppressed)
			reused, err := ReuseRun(t.Context(), store, candidate, source, ReuseOptions{Reporter: reporter})
			if err != nil {
				t.Fatal(err)
			}
			loaded, err := LoadRun(store, reused.RunID)
			if err != nil {
				t.Fatal(err)
			}
			if loaded.Meta.GitHubPostingSuppressed != suppression {
				t.Fatalf("suppression = %q", loaded.Meta.GitHubPostingSuppressed)
			}
			published := &fakeReporter{}
			if err := PublishCompletedRun(t.Context(), store, loaded, PublishOptions{Reporter: published, TargetSHA: candidate.Meta.HeadSHA}); err != nil {
				t.Fatal(err)
			}
			if len(published.posts) != 2 {
				t.Fatalf("published posts = %v", published.posts)
			}
			for _, post := range published.posts {
				if !strings.Contains(post.status.Description, source.RunID) || !strings.Contains(post.status.Description, source.Meta.HeadSHA[:12]) {
					t.Fatalf("publication lost source run/SHA: %+v", post.status)
				}
			}
		})
	}
}

func TestCheckReusableRequiresTheSameProvenResult(t *testing.T) {
	t.Parallel()
	plan := config.ResolvedPlan{Steps: []config.Step{{ID: "check", Command: []string{"/bin/sh", "-c", "exit 0"}}}}
	_, source, candidate := newReusePair(t, plan, config.GitHub{}, "")
	for _, testCase := range []struct {
		name   string
		mutate func(source *RunRecord, candidate *RunRecord)
		want   string
	}{
		{name: "unchanged", mutate: func(*RunRecord, *RunRecord) {}},
		{name: "unfinished", mutate: func(source *RunRecord, _ *RunRecord) { source.Meta.FinishedAt = nil }, want: "did not finish"},
		{name: "failed", mutate: func(source *RunRecord, _ *RunRecord) { source.Summary.Status = string(StepStateFailure) }, want: "not success"},
		{name: "snapshot changed", mutate: func(source *RunRecord, _ *RunRecord) { source.Meta.SnapshotError = "snapshot changed" }, want: "snapshot changed"},
		{name: "itself reused", mutate: func(source *RunRecord, _ *RunRecord) { source.Meta.ReusedFrom = &persistence.ReusedFrom{RunID: "x"} }, want: "reuse the original"},
		{name: "not in a snapshot", mutate: func(source *RunRecord, _ *RunRecord) { source.Meta.TestedTreeHash = "" }, want: "did not run in a snapshot"},
		{name: "other tree", mutate: func(_ *RunRecord, candidate *RunRecord) { candidate.Meta.WorktreeTreeHash = strings.Repeat("6", 40) }, want: "tested tree"},
		{name: "dirty candidate", mutate: func(_ *RunRecord, candidate *RunRecord) { candidate.Meta.DirtyWorktree = true }, want: "tested tree"},
		{name: "other repo", mutate: func(_ *RunRecord, candidate *RunRecord) { candidate.Meta.RepoSlug = "owner/other" }, want: "belongs to"},
		{name: "other config", mutate: func(_ *RunRecord, candidate *RunRecord) { candidate.Meta.ConfigHash = "other" }, want: "another configuration"},
		{name: "other config location", mutate: func(_ *RunRecord, candidate *RunRecord) {
			candidate.Meta.ConfigPath = filepath.Join(candidate.Meta.RepoRoot, "other", "ci.toml")
		}, want: "configuration location"},
		{name: "external config", mutate: func(source *RunRecord, candidate *RunRecord) {
			source.Meta.ConfigPath = "/external/ci.toml"
			candidate.Meta.ConfigPath = "/external/ci.toml"
		}, want: "configuration location"},
		{name: "other plan", mutate: func(_ *RunRecord, candidate *RunRecord) { candidate.Meta.PlanHash = "other" }, want: "another plan"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			sourceCopy, candidateCopy := source, candidate
			testCase.mutate(&sourceCopy, &candidateCopy)
			err := CheckReusable(sourceCopy, candidateCopy)
			if testCase.want == "" {
				if err != nil {
					t.Fatalf("CheckReusable() = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("CheckReusable() = %v, want %q", err, testCase.want)
			}
		})
	}
}

func TestReuseRunPostsTheSourceResultWithoutExecuting(t *testing.T) {
	t.Parallel()
	plan := config.ResolvedPlan{Steps: []config.Step{
		{ID: "check", Command: []string{"/bin/sh", "-c", `printf ran >> "$LOCAL_CI_SOURCE_ROOT/executions"`}},
		{ID: "off", If: "false", Command: []string{"/bin/sh", "-c", "exit 1"}},
	}}
	store, source, candidate := newReusePair(t, plan, config.GitHub{Enabled: true}, "")
	reporter := &fakeReporter{}

	reused, err := ReuseRun(t.Context(), store, candidate, source, ReuseOptions{Reporter: reporter})
	if err != nil {
		t.Fatal(err)
	}
	if executions, _ := os.ReadFile(source.Meta.RepoRoot + "/executions"); string(executions) != "ran" {
		t.Fatalf("executions = %q; reuse must not run the step again", executions)
	}
	if reused.Summary.Status != string(StepStateSuccess) || reused.Meta.ReusedFrom == nil || reused.Meta.ReusedFrom.RunID != source.RunID || reused.Meta.TestedTreeHash != source.Meta.TestedTreeHash {
		t.Fatalf("reused meta = %+v, status = %s", reused.Meta, reused.Summary.Status)
	}
	if states := statusStates(reused.StepStatuses); states["check"] != "success" || states["off"] != "skipped" {
		t.Fatalf("states = %v, want the source's states", states)
	}
	description := "reused: run " + source.RunID + " on " + source.Meta.HeadSHA[:12] + " (same tree)"
	posted := map[string]ghstatus.State{}
	for _, post := range reporter.posts {
		if post.target.SHA != candidate.Meta.HeadSHA || post.status.Description != description {
			t.Fatalf("post = %+v, want %q to %s", post, description, candidate.Meta.HeadSHA)
		}
		posted[post.status.Context] = post.status.State
	}
	if posted["local/check"] != ghstatus.StateSuccess || posted["local/off"] != ghstatus.StateSuccess || posted[config.DefaultAggregateContext] != ghstatus.StateSuccess || len(posted) != 3 {
		t.Fatalf("posted = %v", posted)
	}
	// The reused run is a normal, loadable run directory.
	if _, err := LoadRun(store, reused.RunID); err != nil {
		t.Fatalf("LoadRun(reused) = %v", err)
	}
}

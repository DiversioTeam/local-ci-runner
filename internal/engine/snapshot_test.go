package engine

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/DiversioTeam/local-ci-runner/internal/config"
	ghstatus "github.com/DiversioTeam/local-ci-runner/internal/github"
	"github.com/DiversioTeam/local-ci-runner/internal/gitrepo"
	"github.com/DiversioTeam/local-ci-runner/internal/persistence"
	"github.com/DiversioTeam/local-ci-runner/internal/snapshot"
)

// newSnapshotRun prepares a run in a real Git repository, captured as the CLI captures it.
func newSnapshotRun(t *testing.T, plan config.ResolvedPlan, githubConfig config.GitHub, suppressed string) (string, persistence.Store, RunRecord) {
	t.Helper()
	plan = plan.Clone()
	plan.ApplyDefaults()
	root := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "local-ci@example.com"},
		{"config", "user.name", "Local CI"},
	} {
		gitInDir(t, root, args...)
	}
	writeFile(t, filepath.Join(root, "app.txt"), []byte("committed\n"))
	writeFile(t, filepath.Join(root, ".gitignore"), []byte(".local-ci/\n"))
	gitInDir(t, root, "add", ".")
	gitInDir(t, root, "commit", "-q", "-m", "init")
	writeFile(t, filepath.Join(root, config.DefaultPath), []byte("version = 1\n"))
	captured, err := gitrepo.CaptureTree(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := BuildRunIdentity(root, "owner/repo", captured.HeadSHA, config.DefaultPath, plan)
	if err != nil {
		t.Fatal(err)
	}
	identity.WorktreeTreeHash = captured.WorktreeTreeHash
	if githubConfig.Enabled && githubConfig.AggregateContext == "" {
		githubConfig.AggregateContext = config.DefaultAggregateContext
	}
	store := persistence.NewStore(root)
	run, err := PrepareRun(store, PrepareOptions{
		Identity:                identity,
		Plan:                    plan,
		GitHub:                  githubConfig,
		WorktreeTreeHash:        captured.WorktreeTreeHash,
		GitHubPostingSuppressed: suppressed,
		Now:                     fixedRunTime,
		Random:                  bytes.NewReader(cloneBytes(fixedRunEntropy)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return root, store, run
}

func gitInDir(t *testing.T, dir string, args ...string) {
	t.Helper()
	if output, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}

func requireSnapshotRemoved(t *testing.T, run RunRecord) {
	t.Helper()
	if run.Meta.SnapshotDir == "" {
		t.Fatal("run did not record its snapshot")
	}
	if _, err := os.Lstat(run.Meta.SnapshotDir); !os.IsNotExist(err) {
		t.Fatalf("snapshot %s was not removed: %v", run.Meta.SnapshotDir, err)
	}
}

func TestSnapshotRunTestsTheCapturedTreeWhileTheLiveWorktreeChanges(t *testing.T) {
	t.Parallel()
	// The step edits the live worktree mid-run, as a developer might; then it
	// reports which content it saw and where it ran.
	plan := config.ResolvedPlan{Steps: []config.Step{{
		ID:      "check",
		Command: []string{"/bin/sh", "-c", `printf edited > "$LOCAL_CI_SOURCE_ROOT/app.txt"; cat app.txt > "$LOCAL_CI_STEP_DIR/seen"; pwd -P > "$LOCAL_CI_STEP_DIR/where"`},
	}}}
	root, store, run := newSnapshotRun(t, plan, config.GitHub{}, "")

	executed, err := ExecuteRun(t.Context(), store, run, ExecuteOptions{Snapshot: &SnapshotOptions{}})
	if err != nil {
		t.Fatal(err)
	}
	stepDir := store.StepDir(executed.RunID, 0, "check")
	if seen, _ := os.ReadFile(filepath.Join(stepDir, "seen")); string(seen) != "committed\n" {
		t.Fatalf("step saw %q, want the captured content", seen)
	}
	where, _ := os.ReadFile(filepath.Join(stepDir, "where"))
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	wantDir := snapshot.Path(realRoot, executed.RunID)
	if strings.TrimSpace(string(where)) != wantDir {
		t.Fatalf("step ran in %q, want the snapshot %q", where, wantDir)
	}
	if executed.Summary.Status != string(StepStateSuccess) || executed.Meta.TestedTreeHash != run.Meta.WorktreeTreeHash {
		t.Fatalf("status = %s, tested tree = %q, want success on %q", executed.Summary.Status, executed.Meta.TestedTreeHash, run.Meta.WorktreeTreeHash)
	}
	requireSnapshotRemoved(t, executed)
}

func TestSnapshotIsRemovedAfterFailureAndInterrupt(t *testing.T) {
	t.Parallel()
	t.Run("failure", func(t *testing.T) {
		t.Parallel()
		plan := config.ResolvedPlan{Steps: []config.Step{{ID: "fail", Command: []string{"/bin/sh", "-c", "exit 3"}}}}
		_, store, run := newSnapshotRun(t, plan, config.GitHub{}, "")
		executed, err := ExecuteRun(t.Context(), store, run, ExecuteOptions{Snapshot: &SnapshotOptions{}})
		if err != nil {
			t.Fatal(err)
		}
		if executed.Summary.Status != string(StepStateFailure) || executed.Meta.TestedTreeHash == "" {
			t.Fatalf("status = %s, tested tree = %q; a failed check still ran on the tree", executed.Summary.Status, executed.Meta.TestedTreeHash)
		}
		requireSnapshotRemoved(t, executed)
	})
	t.Run("interrupt", func(t *testing.T) {
		t.Parallel()
		plan := config.ResolvedPlan{Steps: []config.Step{{
			ID:      "wait",
			Command: []string{"/bin/sh", "-c", `: > "$LOCAL_CI_SOURCE_ROOT/started"; exec sleep 30`},
		}}}
		root, store, run := newSnapshotRun(t, plan, config.GitHub{}, "")
		runContext, cancelRun := context.WithCancelCause(t.Context())
		forceStop := make(chan struct{})
		results := make(chan RunRecord, 1)
		go func() {
			executed, err := ExecuteRun(runContext, store, run, ExecuteOptions{ForceStop: forceStop, Snapshot: &SnapshotOptions{}})
			if err != nil {
				t.Error(err)
			}
			results <- executed
		}()
		validateMarkerExists(t, filepath.Join(root, "started"))
		cancelRun(InterruptError{Signal: syscall.SIGINT})
		close(forceStop)
		executed := <-results
		if executed.Summary.Status != string(StepStateInterrupted) || executed.Meta.TestedTreeHash != "" {
			t.Fatalf("status = %s, tested tree = %q; an interrupted run proves no tree", executed.Summary.Status, executed.Meta.TestedTreeHash)
		}
		requireSnapshotRemoved(t, executed)
	})
}

func TestStepThatChangesTheSnapshotFailsTheRunAndCannotBePublished(t *testing.T) {
	t.Parallel()
	plan := config.ResolvedPlan{Steps: []config.Step{
		{ID: "rewrite", Command: []string{"/bin/sh", "-c", "printf rewritten > app.txt"}},
		{ID: "after", Command: []string{"/bin/sh", "-c", `: > "$LOCAL_CI_SOURCE_ROOT/after-ran"`}},
	}}
	t.Run("posting", func(t *testing.T) {
		t.Parallel()
		root, store, run := newSnapshotRun(t, plan, config.GitHub{Enabled: true}, "")
		reporter := &fakeReporter{}
		executed, err := ExecuteRun(t.Context(), store, run, ExecuteOptions{Reporter: reporter, Snapshot: &SnapshotOptions{}})
		if err != nil {
			t.Fatal(err)
		}
		states := statusStates(executed.StepStatuses)
		if states["rewrite"] != string(StepStateSuccess) || states["after"] != string(StepStateBlocked) {
			t.Fatalf("states = %v; the step after the change must not run", states)
		}
		if _, err := os.Stat(filepath.Join(root, "after-ran")); !os.IsNotExist(err) {
			t.Fatal("a step ran on a changed snapshot")
		}
		if executed.Summary.Status != RunStatusSnapshotChanged || executed.Meta.TestedTreeHash != "" || !strings.Contains(executed.Meta.SnapshotError, "tree is") {
			t.Fatalf("status = %s, tested = %q, error = %q", executed.Summary.Status, executed.Meta.TestedTreeHash, executed.Meta.SnapshotError)
		}
		last := reporter.posts[len(reporter.posts)-1].status
		if last.Context != config.DefaultAggregateContext || last.State != ghstatus.StateError || last.Description != aggregateDescriptionSnapshotChanged {
			t.Fatalf("final aggregate = %+v, want error: snapshot changed", last)
		}
		requireSnapshotRemoved(t, executed)
	})
	t.Run("publish", func(t *testing.T) {
		t.Parallel()
		_, store, run := newSnapshotRun(t, plan, config.GitHub{Enabled: true}, "cli_disabled")
		executed, err := ExecuteRun(t.Context(), store, run, ExecuteOptions{Snapshot: &SnapshotOptions{}})
		if err != nil {
			t.Fatal(err)
		}
		loaded, err := LoadRun(store, executed.RunID)
		if err != nil {
			t.Fatalf("a snapshot_changed run must still load: %v", err)
		}
		err = PublishCompletedRun(t.Context(), store, loaded, PublishOptions{Reporter: &fakeReporter{}, TargetSHA: loaded.Meta.HeadSHA})
		if err == nil || !strings.Contains(err.Error(), "cannot be published") {
			t.Fatalf("PublishCompletedRun() = %v, want a refusal", err)
		}
	})
}

func TestSnapshotResumeRerunsStepsFromEarlierAttempts(t *testing.T) {
	t.Parallel()
	plan := config.ResolvedPlan{Steps: []config.Step{
		{ID: "first", Command: []string{"/bin/sh", "-c", `printf x >> "$LOCAL_CI_SOURCE_ROOT/.local-ci/first"`}},
		{ID: "flaky", Needs: []string{"first"}, Command: []string{"/bin/sh", "-c", `test -f "$LOCAL_CI_SOURCE_ROOT/pass"`}},
	}}
	for _, testCase := range []struct {
		name          string
		firstSnapshot bool
	}{
		{name: "first attempt in a snapshot", firstSnapshot: true},
		{name: "first attempt in the live worktree", firstSnapshot: false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			root, store, run := newSnapshotRun(t, plan, config.GitHub{}, "")
			firstOptions := ExecuteOptions{}
			if testCase.firstSnapshot {
				firstOptions.Snapshot = &SnapshotOptions{}
			}
			failed, err := ExecuteRun(t.Context(), store, run, firstOptions)
			if err != nil {
				t.Fatal(err)
			}
			writeFile(t, filepath.Join(root, "pass"), nil)
			resumed, err := ExecuteRun(t.Context(), store, failed, ExecuteOptions{Snapshot: &SnapshotOptions{}})
			if err != nil {
				t.Fatal(err)
			}
			if resumed.Summary.Status != string(StepStateSuccess) || resumed.Meta.TestedTreeHash == "" {
				t.Fatalf("status = %s, tested tree = %q", resumed.Summary.Status, resumed.Meta.TestedTreeHash)
			}
			executions, err := os.ReadFile(filepath.Join(root, ".local-ci", "first"))
			if err != nil || string(executions) != "xx" {
				t.Fatalf("snapshot resume retained earlier execution: %q %v", executions, err)
			}
		})
	}
}

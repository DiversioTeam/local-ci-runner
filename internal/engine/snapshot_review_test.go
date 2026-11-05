package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DiversioTeam/local-ci-runner/internal/config"
	"github.com/DiversioTeam/local-ci-runner/internal/gitrepo"
)

func TestResumeRerunsSuccessesFromAnInvalidSnapshot(t *testing.T) {
	t.Parallel()
	plan := config.ResolvedPlan{Steps: []config.Step{
		{ID: "rewrite", Command: []string{"/bin/sh", "-c", `printf rewritten > app.txt; printf x >> "$LOCAL_CI_RUN_DIR/executions"`}},
		{ID: "after", Command: []string{"true"}},
	}}
	_, store, run := newSnapshotRun(t, plan, config.GitHub{Enabled: true}, "cli_disabled")
	first, err := ExecuteRun(t.Context(), store, run, ExecuteOptions{Snapshot: &SnapshotOptions{}})
	if err != nil {
		t.Fatal(err)
	}
	if first.Summary.Status != RunStatusSnapshotChanged {
		t.Fatalf("first status = %s", first.Summary.Status)
	}
	for _, fromStep := range []string{"", "after"} {
		loaded, err := LoadRun(store, run.RunID)
		if err != nil {
			t.Fatal(err)
		}
		resumed, err := ExecuteRun(t.Context(), store, loaded, ExecuteOptions{Snapshot: &SnapshotOptions{}, FromStep: fromStep})
		if err != nil {
			t.Fatal(err)
		}
		if resumed.Summary.Status != RunStatusSnapshotChanged {
			t.Fatalf("resume from %q trusted invalid successes: %s", fromStep, resumed.Summary.Status)
		}
		loaded, err = LoadRun(store, run.RunID)
		if err != nil {
			t.Fatal(err)
		}
		reporter := &fakeReporter{}
		err = PublishCompletedRun(t.Context(), store, loaded, PublishOptions{Reporter: reporter, TargetSHA: loaded.Meta.HeadSHA})
		if err == nil || !strings.Contains(err.Error(), "cannot be published") || len(reporter.posts) != 0 {
			t.Fatalf("publish invalid resumed snapshot = %v, posts = %v", err, reporter.posts)
		}
	}
	executions, err := os.ReadFile(store.RunFile(run.RunID, "executions"))
	if err != nil || string(executions) != "xxx" {
		t.Fatalf("rewrite executions = %q, %v", executions, err)
	}
}

func TestSnapshotStepDirectoriesCannotReachTheLiveWorktree(t *testing.T) {
	t.Parallel()
	for _, directoryKind := range []string{"relative", "absolute source", "absolute outside", "relative escape", "symlink escape"} {
		t.Run(directoryKind, func(t *testing.T) {
			t.Parallel()
			plan := config.ResolvedPlan{Steps: []config.Step{{ID: "check", Command: []string{"/bin/sh", "-c", `printf x > "$LOCAL_CI_STEP_DIR/ran"; cat app.txt > "$LOCAL_CI_STEP_DIR/seen"`}}}}
			root, store, run := newSnapshotRun(t, plan, config.GitHub{}, "")
			outside := t.TempDir()
			wantFailure := false
			switch directoryKind {
			case "relative":
				run.Plan.Steps[0].Dir = "."
			case "absolute source":
				run.Plan.Steps[0].Dir = root
			case "absolute outside":
				run.Plan.Steps[0].Dir = outside
				wantFailure = true
			case "relative escape":
				run.Plan.Steps[0].Dir = "../../.."
				wantFailure = true
			case "symlink escape":
				if err := os.Symlink(root, filepath.Join(root, "escape")); err != nil {
					t.Fatal(err)
				}
				// Capture the link as part of the plan's tested tree, before executing it.
				gitInDir(t, root, "add", "escape")
				gitInDir(t, root, "commit", "-qm", "add directory link")
				captured, err := gitrepo.CaptureTree(t.Context(), root)
				if err != nil {
					t.Fatal(err)
				}
				run.Meta.HeadSHA, run.Meta.WorktreeTreeHash = captured.HeadSHA, captured.WorktreeTreeHash
				run.Plan.Steps[0].Dir = "escape"
				wantFailure = true
			}
			writeFile(t, filepath.Join(root, "app.txt"), []byte("live edit\n"))
			executed, err := ExecuteRun(t.Context(), store, run, ExecuteOptions{Snapshot: &SnapshotOptions{}})
			if err != nil {
				t.Fatal(err)
			}
			status := executed.StepStatuses[0]
			if wantFailure {
				if status.State != string(StepStateFailure) || !strings.Contains(status.Message, "outside snapshot") {
					t.Fatalf("escaped directory status = %+v", status)
				}
				if _, err := os.Stat(store.StepFile(run.RunID, 0, "check", "ran")); !os.IsNotExist(err) {
					t.Fatalf("escaped step executed: %v", err)
				}
			} else {
				seen, err := os.ReadFile(store.StepFile(run.RunID, 0, "check", "seen"))
				if err != nil || string(seen) != "committed\n" || status.State != string(StepStateSuccess) {
					t.Fatalf("step saw %q, status = %+v, error = %v", seen, status, err)
				}
			}
			requireSnapshotRemoved(t, executed)
		})
	}
}

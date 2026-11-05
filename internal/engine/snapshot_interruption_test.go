package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/DiversioTeam/local-ci-runner/internal/config"
	"github.com/DiversioTeam/local-ci-runner/internal/persistence"
)

func TestInterruptedSnapshotDoesNotReuseUnverifiedSuccesses(t *testing.T) {
	t.Parallel()
	for _, fromStep := range []string{"", "wait"} {
		t.Run("resume from "+fromStep, func(t *testing.T) {
			t.Parallel()
			plan := config.ResolvedPlan{Steps: []config.Step{
				{ID: "rewrite", Command: []string{"/bin/sh", "-c", `while [ ! -f "$LOCAL_CI_RUN_DIR/waiting" ]; do sleep 0.02; done; if [ ! -f "$LOCAL_CI_RUN_DIR/executions" ]; then printf changed > app.txt; test "$(cat app.txt)" = changed || exit 1; fi; printf x >> "$LOCAL_CI_RUN_DIR/executions"`}},
				{ID: "wait", Command: []string{"/bin/sh", "-c", `: > "$LOCAL_CI_RUN_DIR/waiting"; while [ ! -f "$LOCAL_CI_RUN_DIR/release" ]; do sleep 0.02; done`}},
			}}
			_, store, run := newSnapshotRun(t, plan, config.GitHub{Enabled: true}, "cli_disabled")
			run.Meta.MaxParallel = 2
			runContext, cancel := context.WithCancelCause(t.Context())
			defer cancel(nil)
			type result struct {
				run RunRecord
				err error
			}
			results := make(chan result, 1)
			go func() {
				interrupted, err := ExecuteRun(runContext, store, run, ExecuteOptions{Snapshot: &SnapshotOptions{}})
				results <- result{interrupted, err}
			}()
			deadline := time.Now().Add(10 * time.Second)
			succeeded := false
			for time.Now().Before(deadline) {
				status, err := persistence.ReadJSONFile[persistence.StepStatus](store.StepFile(run.RunID, 0, "rewrite", persistence.StatusFile))
				if err == nil && status.State == string(StepStateSuccess) {
					succeeded = true
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			cancel(InterruptError{Signal: os.Interrupt})
			interrupted := <-results
			if !succeeded || interrupted.err != nil {
				t.Fatalf("rewrite success=%v, execution error=%v", succeeded, interrupted.err)
			}
			if interrupted.run.Summary.Status != string(StepStateInterrupted) || interrupted.run.Meta.TestedTreeHash != "" {
				t.Fatalf("first attempt = %+v", interrupted.run.Meta)
			}
			writeFile(t, filepath.Join(run.RunDir, "release"), nil)
			loaded, err := LoadRun(store, run.RunID)
			if err != nil {
				t.Fatal(err)
			}
			resumed, err := ExecuteRun(t.Context(), store, loaded, ExecuteOptions{Snapshot: &SnapshotOptions{}, FromStep: fromStep})
			if err != nil {
				t.Fatal(err)
			}
			executions, err := os.ReadFile(filepath.Join(run.RunDir, "executions"))
			if err != nil || string(executions) != "xx" {
				t.Fatalf("resume retained an unverified success: executions=%q, error=%v", executions, err)
			}
			if resumed.Summary.Status != string(StepStateSuccess) || resumed.Meta.TestedTreeHash != resumed.Meta.WorktreeTreeHash {
				t.Fatalf("rerun did not verify the original tree: %+v", resumed.Meta)
			}
			loaded, err = LoadRun(store, run.RunID)
			if err != nil {
				t.Fatal(err)
			}
			reporter := &fakeReporter{}
			if err := PublishCompletedRun(t.Context(), store, loaded, PublishOptions{Reporter: reporter, TargetSHA: loaded.Meta.HeadSHA}); err != nil {
				t.Fatal(err)
			}
			if len(reporter.posts) != 3 {
				t.Fatalf("verified rerun publication = %v", reporter.posts)
			}
		})
	}
}

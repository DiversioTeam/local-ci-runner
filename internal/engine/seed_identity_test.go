package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DiversioTeam/local-ci-runner/internal/config"
	"github.com/DiversioTeam/local-ci-runner/internal/snapshot"
)

func TestChangedSeedIdentityRefusesExecution(t *testing.T) {
	t.Parallel()
	plan := config.ResolvedPlan{Steps: []config.Step{{ID: "check", Command: []string{"sh", "-c", `touch "$LOCAL_CI_SOURCE_ROOT/.local-ci/executed"`}}}}
	root, store, run := newSnapshotRun(t, plan, config.GitHub{}, "")
	writeFile(t, filepath.Join(root, ".git", "info", "exclude"), []byte(".env\n"))
	writeFile(t, filepath.Join(root, ".env"), []byte("before"))
	seedHash, err := snapshot.GetSeedHash(t.Context(), root, []string{".env"})
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, ".env"), []byte("after"))
	_, err = ExecuteRun(t.Context(), store, run, ExecuteOptions{Snapshot: &SnapshotOptions{Seed: []string{".env"}, AllowPlainCopy: true, ExpectedSeedHash: seedHash}})
	if err == nil || !strings.Contains(err.Error(), "seed inputs changed") {
		t.Fatalf("expected changed-seed refusal: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".local-ci", "executed")); !os.IsNotExist(err) {
		t.Fatalf("step executed before validating copied inputs: %v", err)
	}
	if _, err := os.Stat(snapshot.Path(root, run.RunID)); !os.IsNotExist(err) {
		t.Fatalf("snapshot leaked after seed mismatch: %v", err)
	}
}

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/DiversioTeam/local-ci-runner/internal/gitrepo"
	"github.com/DiversioTeam/local-ci-runner/internal/persistence"
	"github.com/DiversioTeam/local-ci-runner/internal/snapshot"
)

func TestCleanupKeepsSnapshotClaimedByAResumingRunner(t *testing.T) {
	t.Parallel()
	root := newGitRepo(t)
	captured, err := gitrepo.CaptureTree(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	runID := "20260101T000000Z-0badc0de"
	store := persistence.NewStore(root)
	// ExecuteRun claims the PID before creating the snapshot, while the previous finish time remains.
	finished := time.Now().Add(-time.Minute)
	runnerPID := os.Getpid()
	meta := persistence.Meta{RunID: runID, RunnerPID: &runnerPID, FinishedAt: &finished}
	if err := persistence.WriteJSONFile(store.RunFile(runID, persistence.MetaFile), meta); err != nil {
		t.Fatal(err)
	}
	created, err := snapshot.Create(t.Context(), snapshot.Options{RepoRoot: root, RunID: runID, HeadSHA: captured.HeadSHA, TreeHash: captured.WorktreeTreeHash})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = snapshot.Remove(root, created.Dir) })
	if err := snapshot.RemoveStale(root, func(candidate string) bool { return isRunActive(store, candidate) }); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(created.Dir); err != nil {
		t.Fatalf("cleanup removed the live resumed snapshot: %v", err)
	}
}

// A runner killed with SIGKILL cannot remove its snapshot; the next run must.
func TestRunRemovesSnapshotsLeftByDeadRunners(t *testing.T) {
	t.Parallel()
	root := newGitRepo(t)
	writeFile(t, filepath.Join(root, ".local-ci.toml"), []byte("version = 1\n[[steps]]\nid = 'ok'\ncommand = ['true']\n"))
	captured, err := gitrepo.CaptureTree(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	orphan, err := snapshot.Create(t.Context(), snapshot.Options{RepoRoot: root, RunID: "20260101T000000Z-0badc0de", HeadSHA: captured.HeadSHA, TreeHash: captured.WorktreeTreeHash})
	if err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if err := newCLI(&stdout, &stderr, root).runWithContext(t.Context(), nil, []string{"run", "--no-github"}); err != nil {
		t.Fatalf("run: %v\n%s", err, stderr.String())
	}
	entries, err := os.ReadDir(filepath.Join(root, snapshot.Dir))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("snapshots left after the run: %v (orphan was %s)", entries, orphan.Dir)
	}
	if worktrees := gitOutput(t, root, "worktree", "list", "--porcelain"); bytes.Contains([]byte(worktrees), []byte(snapshot.Dir)) {
		t.Fatalf("snapshot worktrees still registered:\n%s", worktrees)
	}
}

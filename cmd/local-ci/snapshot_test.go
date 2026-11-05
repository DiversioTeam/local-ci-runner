package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/DiversioTeam/local-ci-runner/internal/gitrepo"
	"github.com/DiversioTeam/local-ci-runner/internal/snapshot"
)

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

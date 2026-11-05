package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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
	for _, mode := range []string{"execute", "reuse"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			root := newGitRepo(t)
			writeFile(t, filepath.Join(root, ".local-ci.toml"), []byte("version = 1\n[[steps]]\nid = 'ok'\ncommand = ['true']\n"))
			runGit(t, root, "add", ".local-ci.toml")
			runGit(t, root, "commit", "-m", "config")
			if mode == "reuse" {
				var stdout, stderr bytes.Buffer
				if err := newCLI(&stdout, &stderr, root).runWithContext(t.Context(), nil, []string{"run", "--no-github"}); err != nil {
					t.Fatal(err)
				}
			}
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
			if mode == "reuse" && !strings.Contains(stdout.String(), "reused:") {
				t.Fatalf("expected cache hit: %s", stdout.String())
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
		})
	}
}

func TestResumedReuseReportsExecutionWithoutClaimingNoChecksRan(t *testing.T) {
	t.Parallel()
	root := newGitRepo(t)
	writeFile(t, filepath.Join(root, ".local-ci.toml"), []byte("version = 1\n[[steps]]\nid = 'check'\ncommand = ['sh', '-c', 'printf x >> \"$LOCAL_CI_SOURCE_ROOT/.local-ci/executions\"']\n"))
	runGit(t, root, "add", ".local-ci.toml")
	runGit(t, root, "commit", "-m", "config")
	var reused showJSON
	for i := 0; i < 2; i++ {
		var stdout, stderr bytes.Buffer
		if err := newCLI(&stdout, &stderr, root).runWithContext(t.Context(), nil, []string{"run", "--no-github", "--json"}); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(stdout.Bytes(), &reused); err != nil {
			t.Fatal(err)
		}
	}
	if reused.Meta.ReusedFrom == nil {
		t.Fatal("second run did not reuse")
	}
	var stdout, stderr bytes.Buffer
	if err := newCLI(&stdout, &stderr, root).runWithContext(t.Context(), nil, []string{"resume", reused.RunID, "--from-step", "check", "--no-github"}); err != nil {
		t.Fatal(err)
	}
	executions, err := os.ReadFile(filepath.Join(root, ".local-ci", "executions"))
	if err != nil || string(executions) != "xx" {
		t.Fatalf("executions = %q, %v", executions, err)
	}
	if strings.Contains(stdout.String(), "no checks ran") {
		t.Fatalf("resume falsely claimed no execution: %s", stdout.String())
	}
}

func TestChangedSeedInputsRunChecksInsteadOfReusingSuccess(t *testing.T) {
	t.Parallel()
	root := newGitRepo(t)
	writeFile(t, filepath.Join(root, ".gitignore"), []byte(".local-ci/\n.env\n"))
	writeFile(t, filepath.Join(root, ".env"), []byte("good"))
	writeFile(t, filepath.Join(root, ".local-ci.toml"), []byte(`version = 1
[snapshot]
seed = ['.env']
allow_plain_copy = true
[[steps]]
id = 'check'
command = ['sh', '-c', 'printf x >> "$LOCAL_CI_SOURCE_ROOT/.local-ci/executions"; test "$(cat .env)" = good']
`))
	runGit(t, root, "add", ".gitignore", ".local-ci.toml")
	runGit(t, root, "commit", "-m", "seeded check")
	var stdout, stderr bytes.Buffer
	command := newCLI(&stdout, &stderr, root)
	if err := command.runWithContext(t.Context(), nil, []string{"run", "--no-github", "--json"}); err != nil {
		t.Fatalf("first run: %v\n%s", err, stderr.String())
	}
	var first showJSON
	if err := json.Unmarshal(stdout.Bytes(), &first); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	if err := command.runWithContext(t.Context(), nil, []string{"run", "--no-github", "--json"}); err != nil {
		t.Fatal(err)
	}
	var reused showJSON
	if err := json.Unmarshal(stdout.Bytes(), &reused); err != nil {
		t.Fatal(err)
	}
	if reused.Meta.ReusedFrom == nil || reused.Meta.ReusedFrom.RunID != first.RunID {
		t.Fatal("unchanged seed inputs did not reuse the verified result")
	}
	clonedRoot := filepath.Join(t.TempDir(), "checkout")
	runGit(t, root, "clone", root, clonedRoot)
	runGit(t, clonedRoot, "remote", "set-url", "origin", "https://github.com/owner/repo.git")
	writeFile(t, filepath.Join(clonedRoot, ".env"), []byte("good"))
	stdout.Reset()
	stderr.Reset()
	if err := newCLI(&stdout, &stderr, clonedRoot).runWithContext(t.Context(), nil, []string{"run", "--no-github", "--json"}); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(stdout.Bytes(), &reused); err != nil {
		t.Fatal(err)
	}
	if reused.Meta.ReusedFrom == nil || reused.Meta.ReusedFrom.RunID != first.RunID {
		t.Fatal("matching seeds did not reuse across checkouts")
	}
	writeFile(t, filepath.Join(root, ".env"), []byte("bad"))
	stdout.Reset()
	stderr.Reset()
	if err := command.runWithContext(t.Context(), nil, []string{"run", "--no-github", "--json"}); err != errRunFailed {
		t.Fatalf("changed seed reused stale success: error=%v, output=%s", err, stdout.String())
	}
	var second showJSON
	if err := json.Unmarshal(stdout.Bytes(), &second); err != nil {
		t.Fatal(err)
	}
	if second.Meta.ReusedFrom != nil || second.Meta.DirtyWorktree || second.Meta.WorktreeTreeHash != first.Meta.WorktreeTreeHash {
		t.Fatalf("seed change must execute despite an unchanged clean tree: %+v", second.Meta)
	}
	executions, err := os.ReadFile(filepath.Join(root, ".local-ci", "executions"))
	if err != nil || string(executions) != "xx" {
		t.Fatalf("executions=%q, error=%v", executions, err)
	}
}

func TestRunReusesAPassingResultForTheSameTreeOnAnotherCommit(t *testing.T) {
	t.Parallel()
	root := newGitRepo(t)
	// The step counts executions in the live repository, outside the tested tree.
	writeFile(t, filepath.Join(root, ".local-ci.toml"), []byte("version = 1\n[[steps]]\nid = 'check'\ncommand = ['sh', '-c', 'printf x >> \"$LOCAL_CI_SOURCE_ROOT/.local-ci/executions\"']\n"))
	runGit(t, root, "add", ".local-ci.toml")
	runGit(t, root, "commit", "-m", "config")
	executions := func() int {
		content, _ := os.ReadFile(filepath.Join(root, ".local-ci", "executions"))
		return len(content)
	}
	run := func(args ...string) (string, showJSON) {
		t.Helper()
		var stdout, stderr bytes.Buffer
		if err := newCLI(&stdout, &stderr, root).runWithContext(t.Context(), nil, append([]string{"run", "--no-github", "--json"}, args...)); err != nil {
			t.Fatalf("run %v: %v\n%s", args, err, stderr.String())
		}
		var shown showJSON
		if err := json.Unmarshal(stdout.Bytes(), &shown); err != nil {
			t.Fatal(err)
		}
		return stderr.String(), shown
	}

	_, first := run()
	if executions() != 1 || first.Meta.TestedTreeHash == "" {
		t.Fatalf("first run: executions = %d, tested tree = %q", executions(), first.Meta.TestedTreeHash)
	}
	// A new commit with the same tree, as a merge commit or a branch cut from it would have.
	runGit(t, root, "commit", "--allow-empty", "-m", "same tree")
	_, second := run()
	if executions() != 1 || second.Meta.ReusedFrom == nil || second.Meta.ReusedFrom.RunID != first.RunID || second.Status != "success" {
		t.Fatalf("second run: executions = %d, reused_from = %+v, status = %s", executions(), second.Meta.ReusedFrom, second.Status)
	}
	if _, forced := run("--no-reuse"); executions() != 2 || forced.Meta.ReusedFrom != nil {
		t.Fatalf("--no-reuse: executions = %d, reused_from = %+v", executions(), forced.Meta.ReusedFrom)
	}

	// The --no-reuse run replaced the original in the index, so removing the original changes nothing.
	if err := os.RemoveAll(filepath.Join(root, ".local-ci", "runs", first.RunID)); err != nil {
		t.Fatal(err)
	}
	_, latest := run()
	if latest.Meta.ReusedFrom == nil || latest.Meta.ReusedFrom.RunID == first.RunID {
		t.Fatalf("expected reuse of the --no-reuse run, got %+v", latest.Meta.ReusedFrom)
	}
	// A source run that no longer exists is dropped from the index and the checks run.
	if err := os.RemoveAll(filepath.Join(root, ".local-ci", "runs", latest.Meta.ReusedFrom.RunID)); err != nil {
		t.Fatal(err)
	}
	stderr, fallback := run()
	if executions() != 3 || fallback.Meta.ReusedFrom != nil || !strings.Contains(stderr, "not reusing run") {
		t.Fatalf("missing source: executions = %d, reused_from = %+v, stderr = %q", executions(), fallback.Meta.ReusedFrom, stderr)
	}

	expectedExecutions := 3
	for _, unavailable := range []string{"missing", "unreadable"} {
		if unavailable == "unreadable" && os.Geteuid() == 0 {
			continue // Root can read mode-000 files.
		}
		outputPath := filepath.Join(root, ".local-ci", "runs", fallback.RunID, "steps", "001-check", "output.env")
		var damageError error
		if unavailable == "missing" {
			damageError = os.Remove(outputPath)
		} else {
			damageError = os.Chmod(outputPath, 0)
		}
		if damageError != nil {
			t.Fatal(damageError)
		}
		stderr, fallback = run()
		expectedExecutions++
		if executions() != expectedExecutions || fallback.Meta.ReusedFrom != nil || !strings.Contains(stderr, "not reusing run") {
			t.Fatalf("%s output did not fall back: executions=%d, reused=%+v, stderr=%s", unavailable, executions(), fallback.Meta.ReusedFrom, stderr)
		}
		_, repaired := run()
		if executions() != expectedExecutions || repaired.Meta.ReusedFrom == nil || repaired.Meta.ReusedFrom.RunID != fallback.RunID {
			t.Fatalf("%s output left a broken index: executions=%d, reused=%+v", unavailable, executions(), repaired.Meta.ReusedFrom)
		}
	}

	// A dirty worktree always executes.
	writeFile(t, filepath.Join(root, "README.md"), []byte("dirty\n"))
	if _, dirty := run(); executions() != expectedExecutions+1 || dirty.Meta.ReusedFrom != nil {
		t.Fatalf("dirty: executions = %d, reused_from = %+v", executions(), dirty.Meta.ReusedFrom)
	}
}

package snapshot

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/DiversioTeam/local-ci-runner/internal/gitrepo"
)

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	output, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

func writeFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

// newRepo has a tracked file, an ignored virtualenv, and a remote like a real checkout.
func newRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	runGit(t, root, "init", "-q")
	runGit(t, root, "config", "user.email", "local-ci@example.com")
	runGit(t, root, "config", "user.name", "Local CI")
	writeFile(t, filepath.Join(root, "app.txt"), "committed\n")
	writeFile(t, filepath.Join(root, ".gitignore"), ".venv/\n.env\n")
	runGit(t, root, "add", ".")
	runGit(t, root, "commit", "-q", "-m", "init")
	writeFile(t, filepath.Join(root, ".venv", "bin", "tool"), "seeded tool\n")
	return root
}

// newOptions captures the live worktree the way a run does.
func newOptions(t *testing.T, root string) Options {
	t.Helper()
	tree, err := gitrepo.CaptureTree(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	return Options{RepoRoot: root, RunID: "20260627T150405Z-deadbeef", HeadSHA: tree.HeadSHA, TreeHash: tree.WorktreeTreeHash}
}

func listWorktrees(t *testing.T, root string) string {
	t.Helper()
	return runGit(t, root, "worktree", "list", "--porcelain")
}

func TestCreateHoldsTheCapturedTreeWhileTheLiveWorktreeChanges(t *testing.T) {
	t.Parallel()
	root := newRepo(t)
	// Dirty changes are part of the captured tree: a modification and a new file.
	writeFile(t, filepath.Join(root, "app.txt"), "dirty\n")
	writeFile(t, filepath.Join(root, "new.txt"), "untracked\n")
	opts := newOptions(t, root)

	created, err := Create(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = Remove(root, created.Dir) })
	if got := readFile(t, filepath.Join(created.Dir, "app.txt")); got != "dirty\n" {
		t.Fatalf("snapshot app.txt = %q, want the captured dirty content", got)
	}
	if got := readFile(t, filepath.Join(created.Dir, "new.txt")); got != "untracked\n" {
		t.Fatalf("snapshot new.txt = %q", got)
	}

	// Edits, commits, and checkouts in the live worktree no longer reach the run.
	writeFile(t, filepath.Join(root, "app.txt"), "edited during the run\n")
	runGit(t, root, "add", ".")
	runGit(t, root, "commit", "-q", "-m", "during the run")
	if err := created.Verify(t.Context()); err != nil {
		t.Fatalf("live changes reached the snapshot: %v", err)
	}
	if got := readFile(t, filepath.Join(created.Dir, "app.txt")); got != "dirty\n" {
		t.Fatalf("snapshot app.txt = %q after a live commit", got)
	}
}

func TestVerifyRejectsChangesInsideTheSnapshot(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name   string
		change func(t *testing.T, dir string)
		want   string
	}{
		{name: "unchanged", change: func(*testing.T, string) {}},
		{name: "tracked edit", change: func(t *testing.T, dir string) { writeFile(t, filepath.Join(dir, "app.txt"), "changed\n") }, want: "tree is"},
		{name: "unignored new file", change: func(t *testing.T, dir string) { writeFile(t, filepath.Join(dir, "generated.txt"), "x\n") }, want: "tree is"},
		{name: "ignored output", change: func(t *testing.T, dir string) { writeFile(t, filepath.Join(dir, ".venv", "cache"), "x\n") }},
		{name: "HEAD moved", change: func(t *testing.T, dir string) { runGit(t, dir, "commit", "-q", "--allow-empty", "-m", "step") }, want: "HEAD is"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			root := newRepo(t)
			created, err := Create(t.Context(), newOptions(t, root))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = Remove(root, created.Dir) })
			testCase.change(t, created.Dir)
			err = created.Verify(t.Context())
			if testCase.want == "" {
				if err != nil {
					t.Fatalf("Verify() = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("Verify() = %v, want %q", err, testCase.want)
			}
		})
	}
}

func TestSeedClonesIgnoredPathsWithoutLinkingToTheLiveWorktree(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("seeding is supported on macOS and Linux")
	}
	root := newRepo(t)
	opts := newOptions(t, root)
	opts.Seed = []string{".venv", ".env"} // .env does not exist in the live worktree: skipped.
	opts.AllowPlainCopy = true            // Linux CI filesystems may not reflink; the contract is "a real copy".
	var warnings bytes.Buffer
	opts.Warn = &warnings

	created, err := Create(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = Remove(root, created.Dir) })
	seeded := filepath.Join(created.Dir, ".venv", "bin", "tool")
	info, err := os.Lstat(filepath.Join(created.Dir, ".venv"))
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		t.Fatalf("seeded .venv = %v, %v; want a real directory, never a symlink", info, err)
	}
	if got := readFile(t, seeded); got != "seeded tool\n" {
		t.Fatalf("seeded content = %q", got)
	}
	// A write in either copy must not reach the other.
	writeFile(t, seeded, "changed in the run\n")
	if got := readFile(t, filepath.Join(root, ".venv", "bin", "tool")); got != "seeded tool\n" {
		t.Fatalf("a write in the snapshot reached the live worktree: %q", got)
	}
	if _, err := os.Lstat(filepath.Join(created.Dir, ".env")); !os.IsNotExist(err) {
		t.Fatalf("missing seed source produced %v", err)
	}
	if runtime.GOOS == "darwin" && warnings.Len() != 0 {
		t.Fatalf("APFS temp dir should clone without warnings, got %q", warnings.String())
	}
}

func TestSeedRefusesAPathFromTheTestedTree(t *testing.T) {
	t.Parallel()
	root := newRepo(t)
	opts := newOptions(t, root)
	// Seeding a tracked path would replace tested code with live-worktree content.
	opts.Seed = []string{"app.txt"}
	_, err := Create(t.Context(), opts)
	if err == nil || !strings.Contains(err.Error(), "part of the tested tree") {
		t.Fatalf("Create() = %v, want a refusal for a tracked seed", err)
	}
	// A refused snapshot leaves nothing behind.
	if _, statErr := os.Lstat(Path(root, opts.RunID)); !os.IsNotExist(statErr) {
		t.Fatalf("refused snapshot left %s: %v", Path(root, opts.RunID), statErr)
	}
	if strings.Contains(listWorktrees(t, root), opts.RunID) {
		t.Fatal("refused snapshot is still registered as a worktree")
	}
}

func TestSeedAcceptsSelfIgnoringDirectoriesAndRefusesUnignoredOnes(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("seeding is supported on macOS and Linux")
	}
	root := newRepo(t)
	// uv writes `*` into .venv/.gitignore instead of the project's .gitignore.
	writeFile(t, filepath.Join(root, "uv-env", ".gitignore"), "*\n")
	writeFile(t, filepath.Join(root, "uv-env", "lib"), "x\n")
	// An untracked, unignored directory outside the captured tree cannot exist in
	// practice (capture includes it), so add it after capturing.
	opts := newOptions(t, root)
	writeFile(t, filepath.Join(root, "build", "out"), "x\n")
	opts.AllowPlainCopy = true

	opts.Seed = []string{"uv-env"}
	created, err := Create(t.Context(), opts)
	if err != nil {
		t.Fatalf("self-ignoring directory refused: %v", err)
	}
	if err := Remove(root, created.Dir); err != nil {
		t.Fatal(err)
	}

	opts.Seed = []string{"build"}
	if _, err := Create(t.Context(), opts); err == nil || !strings.Contains(err.Error(), "Git does not ignore") {
		t.Fatalf("Create() = %v, want a refusal for an unignored seed", err)
	}
	if _, statErr := os.Lstat(Path(root, opts.RunID)); !os.IsNotExist(statErr) {
		t.Fatalf("refused snapshot left %s: %v", Path(root, opts.RunID), statErr)
	}
}

func TestRemoveStaleKeepsActiveSnapshotsOnly(t *testing.T) {
	t.Parallel()
	root := newRepo(t)
	active, stale := newOptions(t, root), newOptions(t, root)
	active.RunID, stale.RunID = "active-run", "dead-run"
	for _, opts := range []Options{active, stale} {
		if _, err := Create(t.Context(), opts); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { _ = Remove(root, Path(root, active.RunID)) })

	if err := RemoveStale(root, func(runID string) bool { return runID == active.RunID }); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(Path(root, stale.RunID)); !os.IsNotExist(err) {
		t.Fatalf("stale snapshot still exists: %v", err)
	}
	worktrees := listWorktrees(t, root)
	if strings.Contains(worktrees, stale.RunID) || !strings.Contains(worktrees, active.RunID) {
		t.Fatalf("worktrees after cleanup:\n%s", worktrees)
	}
}

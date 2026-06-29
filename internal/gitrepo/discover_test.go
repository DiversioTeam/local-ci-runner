package gitrepo

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseGitHubSlug(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		url  string
		want string
	}{
		{name: "ssh scp", url: "git@github.com:owner/repo.git", want: "owner/repo"},
		{name: "https", url: "https://github.com/owner/repo.git", want: "owner/repo"},
		{name: "ssh url", url: "ssh://git@github.com/owner/repo.git", want: "owner/repo"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := ParseGitHubSlug(testCase.url)
			if err != nil {
				t.Fatalf("ParseGitHubSlug() error = %v", err)
			}
			if got != testCase.want {
				t.Fatalf("ParseGitHubSlug() = %q, want %q", got, testCase.want)
			}
		})
	}
}

func TestDiscoverRejectsRepoWithoutCommits(t *testing.T) {
	t.Parallel()

	repoRoot := t.TempDir()
	runGit(t, repoRoot, "init")
	runGit(t, repoRoot, "remote", "add", "origin", "git@github.com:owner/repo.git")

	_, err := Discover(context.Background(), repoRoot)
	if err == nil {
		t.Fatal("expected error")
	}
	if got, want := err.Error(), "git repo has no commits"; !strings.Contains(got, want) {
		t.Fatalf("error = %v, want substring %q", err, want)
	}
}

func TestDiscover(t *testing.T) {
	t.Parallel()

	repoRoot := t.TempDir()
	runGit(t, repoRoot, "init")
	runGit(t, repoRoot, "config", "user.email", "local-ci@example.com")
	runGit(t, repoRoot, "config", "user.name", "Local CI")
	writeFile(t, filepath.Join(repoRoot, "README.md"), []byte("# repo\n"))
	runGit(t, repoRoot, "add", "README.md")
	runGit(t, repoRoot, "commit", "-m", "init")
	runGit(t, repoRoot, "remote", "add", "origin", "git@github.com:owner/repo.git")

	headSHA := gitOutputForTest(t, repoRoot, "rev-parse", "HEAD")
	canonicalRoot, err := filepath.EvalSymlinks(repoRoot)
	if err != nil {
		t.Fatalf("EvalSymlinks(%s) error = %v", repoRoot, err)
	}
	info, err := Discover(context.Background(), repoRoot)
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if got, want := info.Root, canonicalRoot; got != want {
		t.Fatalf("Root = %q, want %q", got, want)
	}
	if got, want := info.RepoSlug, "owner/repo"; got != want {
		t.Fatalf("RepoSlug = %q, want %q", got, want)
	}
	if got, want := info.HeadSHA, headSHA; got != want {
		t.Fatalf("HeadSHA = %q, want %q", got, want)
	}
	if info.DirtyWorktree {
		t.Fatal("expected clean worktree")
	}
	if info.HeadTreeHash == "" || info.WorktreeTreeHash == "" {
		t.Fatalf("expected tree hashes, got %+v", info)
	}
	if got, want := info.HeadTreeHash, info.WorktreeTreeHash; got != want {
		t.Fatalf("HeadTreeHash = %q, want worktree %q", got, want)
	}
}

func TestDiscoverMarksDirtyWorktree(t *testing.T) {
	t.Parallel()

	repoRoot := t.TempDir()
	runGit(t, repoRoot, "init")
	runGit(t, repoRoot, "config", "user.email", "local-ci@example.com")
	runGit(t, repoRoot, "config", "user.name", "Local CI")
	writeFile(t, filepath.Join(repoRoot, "README.md"), []byte("# repo\n"))
	runGit(t, repoRoot, "add", "README.md")
	runGit(t, repoRoot, "commit", "-m", "init")
	runGit(t, repoRoot, "remote", "add", "origin", "git@github.com:owner/repo.git")
	writeFile(t, filepath.Join(repoRoot, "README.md"), []byte("# dirty\n"))

	info, err := Discover(context.Background(), repoRoot)
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if !info.DirtyWorktree {
		t.Fatal("expected dirty worktree")
	}
	if info.HeadTreeHash == info.WorktreeTreeHash {
		t.Fatalf("expected different tree hashes, got %q", info.HeadTreeHash)
	}
	if len(info.DirtyFiles) != 1 {
		t.Fatalf("dirty file count = %d, want 1", len(info.DirtyFiles))
	}
	if got, want := info.DirtyFiles[0].Path, "README.md"; got != want {
		t.Fatalf("dirty file path = %q, want %q", got, want)
	}
	if got, want := info.DirtyFiles[0].Status, WorktreeFileModified; got != want {
		t.Fatalf("dirty file status = %q, want %q", got, want)
	}
	if info.DirtyFiles[0].BlobHash == "" {
		t.Fatal("expected dirty file blob hash")
	}
}

func TestDiscoverIgnoresLocalCIArtifacts(t *testing.T) {
	t.Parallel()

	repoRoot := t.TempDir()
	runGit(t, repoRoot, "init")
	runGit(t, repoRoot, "config", "user.email", "local-ci@example.com")
	runGit(t, repoRoot, "config", "user.name", "Local CI")
	writeFile(t, filepath.Join(repoRoot, "README.md"), []byte("# repo\n"))
	writeFile(t, filepath.Join(repoRoot, ".gitignore"), []byte(".local-ci/\n"))
	runGit(t, repoRoot, "add", "README.md", ".gitignore")
	runGit(t, repoRoot, "commit", "-m", "init")
	runGit(t, repoRoot, "remote", "add", "origin", "git@github.com:owner/repo.git")
	if err := os.MkdirAll(filepath.Join(repoRoot, ".local-ci", "runs", "run-1"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	writeFile(t, filepath.Join(repoRoot, ".local-ci", "runs", "run-1", "meta.json"), []byte("{}\n"))

	info, err := Discover(context.Background(), repoRoot)
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if info.DirtyWorktree {
		t.Fatal("expected .local-ci artifacts to be ignored")
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()

	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s: %v", args, string(output), err)
	}
}

func gitOutputForTest(t *testing.T, dir string, args ...string) string {
	t.Helper()

	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s: %v", args, string(output), err)
	}
	return strings.TrimSpace(string(output))
}

func writeFile(t *testing.T, path string, content []byte) {
	t.Helper()

	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", path, err)
	}
}

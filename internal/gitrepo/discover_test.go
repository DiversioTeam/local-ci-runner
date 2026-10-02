package gitrepo

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Fixtures and Discover both run git, so neither may see the developer's global config
// (commit signing, hooks, quotePath, default branch).
func TestMain(m *testing.M) {
	os.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	os.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	os.Exit(m.Run())
}

func TestParseGitHubSlug(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		url       string
		want      string
		wantError string
	}{
		{url: "git@github.com:owner/repo.git", want: "owner/repo"},
		{url: "https://github.com/owner/repo.git", want: "owner/repo"},
		{url: "ssh://git@github.com/owner/repo.git", want: "owner/repo"},
		// Statuses always go to github.com, so another host must never yield a slug there.
		{url: "https://gitlab.com/owner/repo.git", wantError: "not on github.com"},
		{url: "ssh://git@github.example.com/owner/repo.git", wantError: "not on github.com"},
		{url: "git@gitlab.com:owner/repo.git", wantError: "not a supported GitHub remote"},
		{url: "https://github.com/owner", wantError: "owner/repo"},
	} {
		t.Run(test.url, func(t *testing.T) {
			got, err := ParseGitHubSlug(test.url)
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("slug = %q, error = %v, want %q", got, err, test.wantError)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("slug = %q, error = %v, want %q", got, err, test.want)
			}
		})
	}
}

func TestDiscoverRootReturnsCancellationCause(t *testing.T) {
	t.Parallel()

	cancellationCause := errors.New("stop git")
	discoverContext, cancelDiscover := context.WithCancelCause(t.Context())
	cancelDiscover(cancellationCause)

	_, err := DiscoverRoot(discoverContext, t.TempDir())
	if !errors.Is(err, cancellationCause) {
		t.Fatalf("DiscoverRoot() error = %v, want %v", err, cancellationCause)
	}
}

func TestDiscoverRejectsRepoWithoutCommits(t *testing.T) {
	t.Parallel()

	repoRoot := t.TempDir()
	runGit(t, repoRoot, "init")
	runGit(t, repoRoot, "remote", "add", "origin", "git@github.com:owner/repo.git")

	_, err := Discover(t.Context(), repoRoot)
	if err == nil || !strings.Contains(err.Error(), "git repo has no commits") {
		t.Fatalf("error = %v, want no commits", err)
	}
}

func TestDiscover(t *testing.T) {
	t.Parallel()

	repoRoot := newCommittedRepo(t)
	headSHA := gitOutputForTest(t, repoRoot, "rev-parse", "HEAD")
	canonicalRoot, err := filepath.EvalSymlinks(repoRoot)
	if err != nil {
		t.Fatalf("EvalSymlinks(%s) error = %v", repoRoot, err)
	}
	info, err := Discover(t.Context(), repoRoot)
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	// Resume and publish compare the canonical root, e.g. /private/var versus /var on macOS.
	if info.Root != canonicalRoot || info.RepoSlug != "owner/repo" || info.HeadSHA != headSHA {
		t.Fatalf("info = %+v", info)
	}
	if info.DirtyWorktree || info.HeadTreeHash == "" || info.HeadTreeHash != info.WorktreeTreeHash {
		t.Fatalf("clean worktree reported as %+v", info)
	}
}

// The snapshot is what publish later compares with HEAD, so every kind of local change must
// change it, including files Git does not track yet.
func TestDiscoverSnapshotsLocalChanges(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name       string
		change     func(t *testing.T, repoRoot string)
		wantPath   string
		wantStatus WorktreeFileStatus
	}{
		{
			name: "modified tracked file",
			change: func(t *testing.T, repoRoot string) {
				writeFile(t, filepath.Join(repoRoot, "README.md"), []byte("# dirty\n"))
			},
			wantPath:   "README.md",
			wantStatus: WorktreeFileModified,
		},
		{
			name: "untracked file",
			change: func(t *testing.T, repoRoot string) {
				writeFile(t, filepath.Join(repoRoot, "new.go"), []byte("package main\n"))
			},
			wantPath:   "new.go",
			wantStatus: WorktreeFileAdded,
		},
		// Git quotes unusual names in line output, and trimming drops edge spaces.
		{
			name:       "untracked non-ASCII name",
			change:     func(t *testing.T, repoRoot string) { writeFile(t, filepath.Join(repoRoot, "café.txt"), []byte("x\n")) },
			wantPath:   "café.txt",
			wantStatus: WorktreeFileAdded,
		},
		{
			name:       "untracked name with a leading space",
			change:     func(t *testing.T, repoRoot string) { writeFile(t, filepath.Join(repoRoot, " lead.txt"), []byte("x\n")) },
			wantPath:   " lead.txt",
			wantStatus: WorktreeFileAdded,
		},
		{
			name: "modified non-ASCII tracked file",
			change: func(t *testing.T, repoRoot string) {
				writeFile(t, filepath.Join(repoRoot, "café.txt"), []byte("x\n"))
				runGit(t, repoRoot, "add", "café.txt")
				runGit(t, repoRoot, "commit", "-m", "add café")
				writeFile(t, filepath.Join(repoRoot, "café.txt"), []byte("y\n"))
			},
			wantPath:   "café.txt",
			wantStatus: WorktreeFileModified,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			repoRoot := newCommittedRepo(t)
			test.change(t, repoRoot)

			info, err := Discover(t.Context(), repoRoot)
			if err != nil {
				t.Fatalf("Discover() error = %v", err)
			}
			if !info.DirtyWorktree || info.HeadTreeHash == info.WorktreeTreeHash {
				t.Fatalf("change left the snapshot equal to HEAD: %+v", info)
			}
			if len(info.DirtyFiles) != 1 || info.DirtyFiles[0].Path != test.wantPath || info.DirtyFiles[0].Status != test.wantStatus || info.DirtyFiles[0].BlobHash == "" {
				t.Fatalf("dirty files = %+v", info.DirtyFiles)
			}
		})
	}
}

// Consumer repos need not gitignore runner artifacts: the runner filters its own directory.
func TestDiscoverIgnoresLocalCIArtifacts(t *testing.T) {
	t.Parallel()

	repoRoot := newCommittedRepo(t)
	if err := os.MkdirAll(filepath.Join(repoRoot, ".local-ci", "runs", "run-1"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	writeFile(t, filepath.Join(repoRoot, ".local-ci", "runs", "run-1", "meta.json"), []byte("{}\n"))

	info, err := Discover(t.Context(), repoRoot)
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if info.DirtyWorktree {
		t.Fatalf("runner artifacts dirtied the snapshot: %+v", info.DirtyFiles)
	}
}

// newCommittedRepo returns a repository with one commit and a github.com origin.
func newCommittedRepo(t *testing.T) string {
	t.Helper()

	repoRoot := t.TempDir()
	runGit(t, repoRoot, "init")
	runGit(t, repoRoot, "config", "user.email", "local-ci@example.com")
	runGit(t, repoRoot, "config", "user.name", "Local CI")
	writeFile(t, filepath.Join(repoRoot, "README.md"), []byte("# repo\n"))
	runGit(t, repoRoot, "add", "README.md")
	runGit(t, repoRoot, "commit", "-m", "init")
	runGit(t, repoRoot, "remote", "add", "origin", "git@github.com:owner/repo.git")
	return repoRoot
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	gitOutputForTest(t, dir, args...)
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

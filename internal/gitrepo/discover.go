package gitrepo

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type WorktreeFileStatus string

const (
	WorktreeFileAdded      WorktreeFileStatus = "added"
	WorktreeFileModified   WorktreeFileStatus = "modified"
	WorktreeFileDeleted    WorktreeFileStatus = "deleted"
	WorktreeFileRenamed    WorktreeFileStatus = "renamed"
	WorktreeFileCopied     WorktreeFileStatus = "copied"
	WorktreeFileTypeChange WorktreeFileStatus = "type_changed"
	WorktreeFileUnmerged   WorktreeFileStatus = "unmerged"
)

type WorktreeFile struct {
	Path         string
	Status       WorktreeFileStatus
	PreviousPath string
	BlobHash     string
}

// Info is the git snapshot the runner uses as its trust boundary.
//
// First principle: a local run is only reusable or publishable when we can say
// exactly which code snapshot produced it. HEAD SHA alone is not enough for a
// dirty worktree, so we also record the HEAD tree, the effective worktree tree,
// and a small manifest of the dirty files that produced that snapshot.
type Info struct {
	Root             string
	RepoSlug         string
	HeadSHA          string
	HeadTreeHash     string
	WorktreeTreeHash string
	DirtyWorktree    bool
	DirtyFiles       []WorktreeFile
}

func DiscoverRoot(ctx context.Context, startDir string) (string, error) {
	ctx = resolveContext(ctx)

	root, err := gitOutput(ctx, startDir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve repo root: %w", err)
	}

	return absRoot, nil
}

func Discover(ctx context.Context, startDir string) (Info, error) {
	ctx = resolveContext(ctx)

	absRoot, err := DiscoverRoot(ctx, startDir)
	if err != nil {
		return Info{}, err
	}

	headSHA, err := gitOutput(ctx, absRoot, "rev-parse", "HEAD")
	if err != nil {
		if strings.Contains(err.Error(), "unknown revision") || strings.Contains(err.Error(), "ambiguous argument 'HEAD'") {
			return Info{}, fmt.Errorf("git repo has no commits; create an initial commit before running local-ci")
		}
		return Info{}, err
	}
	headTreeHash, err := gitOutput(ctx, absRoot, "rev-parse", "HEAD^{tree}")
	if err != nil {
		return Info{}, err
	}
	snapshot, err := worktreeSnapshot(ctx, absRoot)
	if err != nil {
		return Info{}, err
	}
	remoteName, err := repoRemoteName(ctx, absRoot)
	if err != nil {
		return Info{}, err
	}
	remoteURL, err := gitOutput(ctx, absRoot, "remote", "get-url", remoteName)
	if err != nil {
		return Info{}, err
	}
	repoSlug, err := ParseGitHubSlug(remoteURL)
	if err != nil {
		return Info{}, err
	}

	return Info{
		Root:             absRoot,
		RepoSlug:         repoSlug,
		HeadSHA:          headSHA,
		HeadTreeHash:     headTreeHash,
		WorktreeTreeHash: snapshot.treeHash,
		DirtyWorktree:    headTreeHash != snapshot.treeHash,
		DirtyFiles:       snapshot.dirtyFiles,
	}, nil
}

func ParseGitHubSlug(remoteURL string) (string, error) {
	trimmed := strings.TrimSuffix(strings.TrimSpace(remoteURL), ".git")

	switch {
	case strings.HasPrefix(trimmed, "git@github.com:"):
		return slugFromPath(strings.TrimPrefix(trimmed, "git@github.com:"))
	case strings.HasPrefix(trimmed, "ssh://") || strings.HasPrefix(trimmed, "https://") || strings.HasPrefix(trimmed, "http://"):
		parsed, err := url.Parse(trimmed)
		if err != nil {
			return "", fmt.Errorf("parse remote URL %q: %w", remoteURL, err)
		}
		if !strings.EqualFold(parsed.Hostname(), "github.com") {
			return "", fmt.Errorf("remote URL %q is not on github.com", remoteURL)
		}
		return slugFromPath(parsed.Path)
	default:
		return "", fmt.Errorf("remote URL %q is not a supported GitHub remote", remoteURL)
	}
}

func slugFromPath(path string) (string, error) {
	trimmed := strings.Trim(path, "/")
	parts := strings.Split(trimmed, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", fmt.Errorf("GitHub path %q does not match owner/repo", path)
	}
	return parts[0] + "/" + parts[1], nil
}

func repoRemoteName(ctx context.Context, repoRoot string) (string, error) {
	branch, err := gitOutput(ctx, repoRoot, "branch", "--show-current")
	if err == nil && branch != "" {
		remoteName, remoteErr := gitOutput(ctx, repoRoot, "config", "--get", "branch."+branch+".remote")
		if remoteErr == nil && remoteName != "" {
			return remoteName, nil
		}
	}

	remotesOutput, err := gitOutput(ctx, repoRoot, "remote")
	if err != nil {
		return "", err
	}
	remotes := nonEmptyLines(remotesOutput)
	for _, remote := range remotes {
		if remote == "origin" {
			return remote, nil
		}
	}
	if len(remotes) == 1 {
		return remotes[0], nil
	}

	return "", fmt.Errorf("could not determine git remote")
}

func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	return gitOutputWithEnv(ctx, dir, nil, args...)
}

func gitOutputWithEnv(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	if env != nil {
		cmd.Env = env
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
		}
		return "", fmt.Errorf("git %s: %s: %w", strings.Join(args, " "), message, err)
	}

	return strings.TrimSpace(string(output)), nil
}

type worktreeSnapshotResult struct {
	treeHash   string
	dirtyFiles []WorktreeFile
}

// worktreeSnapshot computes the effective tree for the current working copy and
// a small manifest of the files that made it differ from HEAD.
//
// Why both exist:
// - tree hash answers the trust question: "is this the exact same snapshot?"
// - dirty file manifest answers the debugging question: "what was dirty?"
func worktreeSnapshot(ctx context.Context, repoRoot string) (worktreeSnapshotResult, error) {
	indexFile, err := os.CreateTemp("", "local-ci-index-*.tmp")
	if err != nil {
		return worktreeSnapshotResult{}, fmt.Errorf("create temp index: %w", err)
	}
	indexPath := indexFile.Name()
	if err := indexFile.Close(); err != nil {
		_ = os.Remove(indexPath)
		return worktreeSnapshotResult{}, fmt.Errorf("close temp index: %w", err)
	}
	defer func() {
		_ = os.Remove(indexPath)
	}()

	env := append(os.Environ(), "GIT_INDEX_FILE="+indexPath)
	if _, err := gitOutputWithEnv(ctx, repoRoot, env, "read-tree", "HEAD"); err != nil {
		return worktreeSnapshotResult{}, err
	}
	if _, err := gitOutputWithEnv(ctx, repoRoot, env, "add", "-u", "--", "."); err != nil {
		return worktreeSnapshotResult{}, err
	}
	untracked, err := listUntrackedFiles(ctx, repoRoot)
	if err != nil {
		return worktreeSnapshotResult{}, err
	}
	if len(untracked) > 0 {
		args := append([]string{"add", "--"}, untracked...)
		if _, err := gitOutputWithEnv(ctx, repoRoot, env, args...); err != nil {
			return worktreeSnapshotResult{}, err
		}
	}
	treeHash, err := gitOutputWithEnv(ctx, repoRoot, env, "write-tree")
	if err != nil {
		return worktreeSnapshotResult{}, err
	}
	dirtyFiles, err := diffWorktreeFiles(ctx, repoRoot, env)
	if err != nil {
		return worktreeSnapshotResult{}, err
	}
	return worktreeSnapshotResult{treeHash: treeHash, dirtyFiles: dirtyFiles}, nil
}

func diffWorktreeFiles(ctx context.Context, repoRoot string, env []string) ([]WorktreeFile, error) {
	output, err := gitOutputWithEnv(ctx, repoRoot, env, "diff-index", "--cached", "--name-status", "-M", "HEAD", "--")
	if err != nil {
		return nil, err
	}
	lines := nonEmptyLines(output)
	files := make([]WorktreeFile, 0, len(lines))
	for _, line := range lines {
		parts := strings.Split(line, "\t")
		if len(parts) < 2 {
			return nil, fmt.Errorf("parse git diff-index line %q", line)
		}
		status, err := worktreeFileStatus(parts[0])
		if err != nil {
			return nil, err
		}
		item := WorktreeFile{Status: status}
		switch status {
		case WorktreeFileRenamed, WorktreeFileCopied:
			if len(parts) < 3 {
				return nil, fmt.Errorf("parse git diff-index rename/copy line %q", line)
			}
			item.PreviousPath = parts[1]
			item.Path = parts[2]
		default:
			item.Path = parts[1]
		}
		if item.Status != WorktreeFileDeleted && item.Status != WorktreeFileUnmerged {
			blobHash, blobErr := indexedBlobHash(ctx, repoRoot, env, item.Path)
			if blobErr != nil {
				return nil, blobErr
			}
			item.BlobHash = blobHash
		}
		files = append(files, item)
	}
	return files, nil
}

func worktreeFileStatus(token string) (WorktreeFileStatus, error) {
	if token == "" {
		return "", fmt.Errorf("empty git worktree status token")
	}
	switch token[0] {
	case 'A':
		return WorktreeFileAdded, nil
	case 'M':
		return WorktreeFileModified, nil
	case 'D':
		return WorktreeFileDeleted, nil
	case 'R':
		return WorktreeFileRenamed, nil
	case 'C':
		return WorktreeFileCopied, nil
	case 'T':
		return WorktreeFileTypeChange, nil
	case 'U':
		return WorktreeFileUnmerged, nil
	default:
		return "", fmt.Errorf("unsupported git worktree status %q", token)
	}
}

func indexedBlobHash(ctx context.Context, repoRoot string, env []string, path string) (string, error) {
	output, err := gitOutputWithEnv(ctx, repoRoot, env, "ls-files", "-s", "--", path)
	if err != nil {
		return "", err
	}
	fields := strings.Fields(output)
	if len(fields) < 2 {
		return "", fmt.Errorf("parse git ls-files output for %q: %q", path, output)
	}
	return fields[1], nil
}

func listUntrackedFiles(ctx context.Context, repoRoot string) ([]string, error) {
	output, err := gitOutput(ctx, repoRoot, "ls-files", "--others", "--exclude-standard")
	if err != nil {
		return nil, err
	}
	paths := nonEmptyLines(output)
	result := make([]string, 0, len(paths))
	for _, path := range paths {
		if path == ".local-ci" || strings.HasPrefix(path, ".local-ci/") {
			continue
		}
		result = append(result, path)
	}
	return result, nil
}

func nonEmptyLines(text string) []string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	result := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		result = append(result, strings.TrimSpace(line))
	}
	return result
}

func resolveContext(ctx context.Context) context.Context {
	if ctx != nil {
		return ctx
	}
	return context.Background()
}

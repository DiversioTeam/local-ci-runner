package snapshot

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/DiversioTeam/local-ci-runner/internal/gitrepo"
)

// Dir is where snapshots live, relative to the repository root. Like run
// directories, it is excluded from the worktree snapshot.
const Dir = ".local-ci/snapshots"

// removeTimeout bounds cleanup, which must still run after an interrupt.
const removeTimeout = 2 * time.Minute

// Options describes the snapshot of one run.
type Options struct {
	RepoRoot string
	RunID    string
	// HeadSHA is the commit the snapshot checks out.
	HeadSHA string
	// TreeHash is the working-copy tree to materialize. It equals HEAD's tree
	// for a clean worktree; for a dirty one it is the captured dirty tree.
	TreeHash string
	// Seed are Git-ignored paths cloned from the live worktree.
	Seed           []string
	AllowPlainCopy bool
	// Warn receives one line per seed path that was fully copied.
	Warn io.Writer
}

// Snapshot is a created snapshot. Remove it when the run ends.
type Snapshot struct {
	RepoRoot string
	Dir      string
	HeadSHA  string
	TreeHash string
}

// Path returns the snapshot directory for a run.
func Path(repoRoot string, runID string) string {
	return filepath.Join(repoRoot, Dir, runID)
}

// Create checks out opts.TreeHash into a fresh detached worktree, seeds it,
// and proves it holds exactly that tree. On error nothing is left behind.
func Create(ctx context.Context, opts Options) (created Snapshot, err error) {
	dir := Path(opts.RepoRoot, opts.RunID)
	// A resumed run gets a new snapshot; an old one for the same run is stale.
	if err := Remove(opts.RepoRoot, dir); err != nil {
		return Snapshot{}, err
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return Snapshot{}, fmt.Errorf("create snapshot parent: %w", err)
	}
	if _, err := git(ctx, opts.RepoRoot, "worktree", "add", "--detach", "--no-checkout", dir, opts.HeadSHA); err != nil {
		_ = Remove(opts.RepoRoot, dir)
		return Snapshot{}, fmt.Errorf("create snapshot worktree: %w", err)
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, Remove(opts.RepoRoot, dir))
		}
	}()
	// Check out the tree being verified, then point the index back at HEAD so
	// dirty changes look exactly as they did in the live worktree.
	if _, err := git(ctx, dir, "read-tree", "--reset", "-u", opts.TreeHash); err != nil {
		return Snapshot{}, fmt.Errorf("check out snapshot tree: %w", err)
	}
	if _, err := git(ctx, dir, "read-tree", "HEAD"); err != nil {
		return Snapshot{}, fmt.Errorf("reset snapshot index: %w", err)
	}
	created = Snapshot{RepoRoot: opts.RepoRoot, Dir: dir, HeadSHA: opts.HeadSHA, TreeHash: opts.TreeHash}
	if err := created.Verify(ctx); err != nil {
		return Snapshot{}, err
	}
	for _, path := range opts.Seed {
		if err := seed(ctx, opts, dir, path); err != nil {
			return Snapshot{}, err
		}
	}
	// Seeds must be Git-ignored (directly, or like uv's .venv through their own
	// .gitignore), so the tree must be unchanged; prove it.
	if len(opts.Seed) > 0 {
		if err := created.Verify(ctx); err != nil {
			return Snapshot{}, fmt.Errorf("snapshot.seed adds files Git does not ignore; seed only ignored paths: %w", err)
		}
	}
	return created, nil
}

// Verify fails unless the snapshot still has its HEAD and working-copy tree.
func (s Snapshot) Verify(ctx context.Context) error {
	current, err := gitrepo.CaptureTree(ctx, s.Dir)
	if err != nil {
		return fmt.Errorf("read snapshot tree: %w", err)
	}
	if current.HeadSHA != s.HeadSHA {
		return fmt.Errorf("snapshot changed: HEAD is %s, expected %s", current.HeadSHA, s.HeadSHA)
	}
	if current.WorktreeTreeHash != s.TreeHash {
		return fmt.Errorf("snapshot changed: tree is %s, expected %s (a step modified tracked or unignored files)", current.WorktreeTreeHash, s.TreeHash)
	}
	return nil
}

// Remove deletes a snapshot and its worktree registration. It ignores the
// caller's context so cleanup still happens after an interrupt, and it is a
// no-op when the snapshot does not exist.
func Remove(repoRoot string, dir string) error {
	ctx, cancel := context.WithTimeout(context.Background(), removeTimeout)
	defer cancel()
	if _, statErr := os.Lstat(dir); statErr == nil {
		// Best effort: --force also drops a worktree with modified files.
		_, _ = git(ctx, repoRoot, "worktree", "remove", "--force", "--force", dir)
		if err := os.RemoveAll(dir); err != nil {
			return fmt.Errorf("remove snapshot %s: %w", dir, err)
		}
	}
	if _, err := git(ctx, repoRoot, "worktree", "prune"); err != nil {
		return fmt.Errorf("prune snapshot worktrees: %w", err)
	}
	return nil
}

// RemoveStale removes snapshots whose run is no longer executing, such as one
// left by a runner killed with SIGKILL. isActive reports whether a run ID
// still belongs to a live runner.
func RemoveStale(repoRoot string, isActive func(runID string) bool) error {
	entries, err := os.ReadDir(filepath.Join(repoRoot, Dir))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("list snapshots: %w", err)
	}
	var removeErrors []error
	for _, entry := range entries {
		if isActive(entry.Name()) {
			continue
		}
		removeErrors = append(removeErrors, Remove(repoRoot, filepath.Join(repoRoot, Dir, entry.Name())))
	}
	return errors.Join(removeErrors...)
}

func seed(ctx context.Context, opts Options, dir string, path string) error {
	source := filepath.Join(opts.RepoRoot, path)
	target := filepath.Join(dir, path)
	if _, err := os.Lstat(source); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("snapshot.seed %q: %w", path, err)
	}
	if _, err := os.Lstat(target); err == nil {
		return fmt.Errorf("snapshot.seed %q is part of the tested tree; seed only Git-ignored paths", path)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("snapshot.seed %q: %w", path, err)
	}
	cloneError := cloneTree(ctx, source, target)
	if cloneError == nil {
		return validateSeedLinks(opts.RepoRoot, dir, target)
	}
	if err := os.RemoveAll(target); err != nil {
		return fmt.Errorf("snapshot.seed %q: remove partial clone: %w", path, err)
	}
	if !opts.AllowPlainCopy {
		return fmt.Errorf("snapshot.seed %q: clone failed: %w (set snapshot.allow_plain_copy = true to permit a full copy)", path, cloneError)
	}
	if err := plainCopy(ctx, source, target); err != nil {
		_ = os.RemoveAll(target)
		return fmt.Errorf("snapshot.seed %q: copy: %w", path, err)
	}
	warn(opts.Warn, "snapshot: copied %s without copy-on-write (%v)", path, cloneError)
	return validateSeedLinks(opts.RepoRoot, dir, target)
}

// Copy-on-write preserves symlinks; links must not lend a snapshot access to live source.
func validateSeedLinks(repoRoot string, snapshotDir string, seedPath string) error {
	sourceRoot, err := filepath.EvalSymlinks(repoRoot)
	if err != nil {
		return err
	}
	snapshotRoot, err := filepath.EvalSymlinks(snapshotDir)
	if err != nil {
		return err
	}
	return filepath.WalkDir(seedPath, func(path string, entry fs.DirEntry, walkError error) error {
		if walkError != nil {
			return walkError
		}
		if entry.Type()&os.ModeSymlink == 0 {
			return nil
		}
		target, err := filepath.EvalSymlinks(path)
		if err != nil {
			return fmt.Errorf("seed symlink %q cannot be checked: %w", path, err)
		}
		if target == snapshotRoot || strings.HasPrefix(target, snapshotRoot+string(filepath.Separator)) {
			return nil
		}
		if target == sourceRoot || strings.HasPrefix(target, sourceRoot+string(filepath.Separator)) {
			return fmt.Errorf("seed symlink %q points into the live worktree", path)
		}
		return nil
	})
}

func warn(writer io.Writer, format string, args ...any) {
	if writer != nil {
		_, _ = fmt.Fprintf(writer, format+"\n", args...)
	}
}

func git(ctx context.Context, dir string, args ...string) (string, error) {
	return run(ctx, dir, "git", args...)
}

func run(ctx context.Context, dir string, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.WaitDelay = time.Second
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s %s: %s: %w", name, strings.Join(args, " "), strings.TrimSpace(string(output)), err)
	}
	return strings.TrimSpace(string(output)), nil
}

// Package resultcache indexes successful, snapshot-tested runs by the code they
// tested, so a later run of the same tree, configuration, and plan on any
// branch, commit, or worktree of the repository can reuse the result instead
// of executing the checks again.
package resultcache

// Package snapshot creates the immutable checkout a run's steps execute in.
//
// A run verifies one exact tree. Steps used to run in the live worktree while
// statuses posted to the SHA captured at the start, so an edit, checkout or
// commit during a run could produce a green status for code that was never
// tested (local-ci-runner#11). A snapshot is a detached Git worktree of that
// tree; the live worktree can change freely while it runs.
package snapshot

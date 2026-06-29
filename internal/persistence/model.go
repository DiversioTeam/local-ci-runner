package persistence

import "time"

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
	Path         string             `json:"path"`
	Status       WorktreeFileStatus `json:"status"`
	PreviousPath string             `json:"previous_path,omitempty"`
	BlobHash     string             `json:"blob_hash,omitempty"`
}

type Meta struct {
	RunID                   string         `json:"run_id"`
	RepoRoot                string         `json:"repo_root"`
	RepoSlug                string         `json:"repo_slug"`
	HeadSHA                 string         `json:"head_sha"`
	ConfigPath              string         `json:"config_path"`
	ConfigHash              string         `json:"config_hash"`
	PlanHash                string         `json:"plan_hash"`
	GitHubEnabled           bool           `json:"github_enabled,omitempty"`
	GitHubAggregateContext  string         `json:"github_aggregate_context,omitempty"`
	CreatedAt               time.Time      `json:"created_at"`
	StartedAt               *time.Time     `json:"started_at,omitempty"`
	FinishedAt              *time.Time     `json:"finished_at,omitempty"`
	RunnerPID               *int           `json:"runner_pid,omitempty"`
	HeadTreeHash            string         `json:"head_tree_hash,omitempty"`
	WorktreeTreeHash        string         `json:"worktree_tree_hash,omitempty"`
	DirtyWorktree           bool           `json:"dirty_worktree,omitempty"`
	DirtyFiles              []WorktreeFile `json:"dirty_files,omitempty"`
	GitHubPostingSuppressed string         `json:"github_posting_suppressed,omitempty"`
}

type Summary struct {
	RunID          string            `json:"run_id"`
	Status         string            `json:"status"`
	StartedAt      *time.Time        `json:"started_at,omitempty"`
	FinishedAt     *time.Time        `json:"finished_at,omitempty"`
	DurationMillis int64             `json:"duration_millis,omitempty"`
	Steps          []StepSummary     `json:"steps,omitempty"`
	Counts         map[string]int    `json:"counts,omitempty"`
	Metadata       map[string]string `json:"metadata,omitempty"`
}

type StepSummary struct {
	StepID        string `json:"step_id"`
	State         string `json:"state"`
	GitHubContext string `json:"github_context,omitempty"`
}

type StepStatus struct {
	StepID         string     `json:"step_id"`
	StepName       string     `json:"step_name,omitempty"`
	State          string     `json:"state"`
	Index          int        `json:"index"`
	Needs          []string   `json:"needs,omitempty"`
	GitHubContext  string     `json:"github_context,omitempty"`
	StartedAt      *time.Time `json:"started_at,omitempty"`
	FinishedAt     *time.Time `json:"finished_at,omitempty"`
	DurationMillis int64      `json:"duration_millis,omitempty"`
	ExitCode       *int       `json:"exit_code,omitempty"`
	StdoutLog      string     `json:"stdout_log,omitempty"`
	StderrLog      string     `json:"stderr_log,omitempty"`
	CombinedLog    string     `json:"combined_log,omitempty"`
	OutputEnv      string     `json:"output_env,omitempty"`
}

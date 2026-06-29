package persistence

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestMetaOmitsUnsetOptionalFields(t *testing.T) {
	t.Parallel()

	meta := Meta{
		RunID:      "run-123",
		RepoRoot:   "/tmp/repo",
		RepoSlug:   "owner/repo",
		HeadSHA:    "abc123",
		ConfigPath: ".local-ci.toml",
		ConfigHash: "cfg",
		PlanHash:   "plan",
		CreatedAt:  time.Date(2026, 6, 27, 12, 0, 0, 0, time.UTC),
	}

	payload, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	text := string(payload)

	for _, field := range []string{"started_at", "finished_at", "runner_pid", "head_tree_hash", "worktree_tree_hash", "dirty_worktree", "dirty_files", "github_posting_suppressed"} {
		if strings.Contains(text, field) {
			t.Fatalf("unexpected optional field %q in %s", field, text)
		}
	}
}

func TestStepStatusOmitsUnsetOptionalFields(t *testing.T) {
	t.Parallel()

	status := StepStatus{
		StepID: "lint",
		State:  "success",
		Index:  0,
	}

	payload, err := json.Marshal(status)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	text := string(payload)

	if strings.Contains(text, "started_at") || strings.Contains(text, "finished_at") || strings.Contains(text, "exit_code") {
		t.Fatalf("unexpected optional fields in %s", text)
	}
}

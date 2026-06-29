package planner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DiversioTeam/local-ci-runner/internal/config"
)

func TestExecutePlanner(t *testing.T) {
	t.Parallel()

	repoRoot := t.TempDir()
	scriptPath := filepath.Join(repoRoot, "planner.sh")
	script := "#!/bin/sh\nprintf 'planner-log\\n' >&2\ncat <<'JSON'\n{\"env\":{\"CHANGED_SCOPE\":\"python\"},\"steps\":[{\"id\":\"lint\",\"command\":[\"/bin/sh\",\"-c\",\"printf \\\"%s %s\\\\n\\\" \\\"$LOCAL_CI_GITHUB_REPO\\\" \\\"$LOCAL_CI_GITHUB_SHA\\\"\"]}]}\nJSON\n"
	writeExecutable(t, scriptPath, script)

	result, err := Execute(context.Background(), repoRoot, "owner/repo", "abc123", filepath.Join(repoRoot, config.DefaultPath), config.Planner{Command: []string{scriptPath}})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got, want := result.Log, "planner-log\n"; got != want {
		t.Fatalf("Log = %q, want %q", got, want)
	}
	if got, want := result.Plan.Env["CHANGED_SCOPE"], "python"; got != want {
		t.Fatalf("plan env = %q, want %q", got, want)
	}
	if len(result.Plan.Steps) != 1 {
		t.Fatalf("step count = %d, want 1", len(result.Plan.Steps))
	}
	if got, want := result.Plan.Steps[0].Dir, config.DefaultWorkingDir; got != want {
		t.Fatalf("step dir = %q, want %q", got, want)
	}
}

func TestExecutePlannerRejectsInvalidOutput(t *testing.T) {
	t.Parallel()

	repoRoot := t.TempDir()
	scriptPath := filepath.Join(repoRoot, "planner.sh")
	writeExecutable(t, scriptPath, "#!/bin/sh\nprintf 'not-json\\n'\n")

	_, err := Execute(context.Background(), repoRoot, "owner/repo", "abc123", filepath.Join(repoRoot, config.DefaultPath), config.Planner{Command: []string{scriptPath}})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "decode planner output") {
		t.Fatalf("error = %v", err)
	}
}

func writeExecutable(t *testing.T, path string, content string) {
	t.Helper()

	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", path, err)
	}
}

package planner

import (
	"context"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DiversioTeam/local-ci-runner/internal/config"
)

func TestExecutePlanner(t *testing.T) {
	t.Parallel()

	repoRoot := t.TempDir()
	configPath := filepath.Join(repoRoot, config.DefaultPath)
	scriptPath := filepath.Join(repoRoot, "planner.sh")
	// The unquoted heredoc expands the environment the planner actually received.
	writeScript(t, scriptPath, `printf 'planner-log\n' >&2
cat <<JSON
{"env":{"ROOT":"$LOCAL_CI_REPO_ROOT","CFG":"$LOCAL_CI_CONFIG","REPO":"$LOCAL_CI_GITHUB_REPO","SHA":"$LOCAL_CI_GITHUB_SHA","MODE":"$MODE"},"steps":[{"id":"lint","command":["true"]}]}
JSON
`)

	result, err := Execute(t.Context(), repoRoot, "owner/repo", "abc123", configPath, config.Planner{
		Command: []string{"/bin/sh", scriptPath},
		// Runner identity wins over planner env, so a config cannot spoof the commit under test.
		Env: map[string]string{"MODE": "fast", "LOCAL_CI_GITHUB_SHA": "spoofed"},
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got, want := result.Log, "planner-log\n"; got != want {
		t.Fatalf("Log = %q, want %q", got, want)
	}
	want := map[string]string{"ROOT": repoRoot, "CFG": configPath, "REPO": "owner/repo", "SHA": "abc123", "MODE": "fast"}
	if !maps.Equal(result.Plan.Env, want) {
		t.Fatalf("planner saw env %v, want %v", result.Plan.Env, want)
	}
	if got, want := result.Plan.Steps[0].Dir, config.DefaultWorkingDir; len(result.Plan.Steps) != 1 || got != want {
		t.Fatalf("steps = %+v, want one step with default dir", result.Plan.Steps)
	}
}

func TestExecutePlannerReturnsCancellationCause(t *testing.T) {
	t.Parallel()

	repoRoot := t.TempDir()
	scriptPath := filepath.Join(repoRoot, "planner.sh")
	writeScript(t, scriptPath, "exit 0\n")
	cancellationCause := errors.New("stop planner")
	plannerContext, cancelPlanner := context.WithCancelCause(t.Context())
	cancelPlanner(cancellationCause)

	_, err := Execute(plannerContext, repoRoot, "owner/repo", "abc123", filepath.Join(repoRoot, config.DefaultPath), config.Planner{Command: []string{"/bin/sh", scriptPath}})
	if !errors.Is(err, cancellationCause) {
		t.Fatalf("Execute() error = %v, want %v", err, cancellationCause)
	}
}

func TestExecutePlannerRejectsInvalidOutput(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		output string
	}{
		{name: "not JSON", output: "not-json"},
		// A misspelled field would silently drop the ordering edge between parallel steps.
		{name: "unknown step field", output: `{"steps":[{"id":"a","command":["true"]},{"id":"b","command":["true"],"need":["a"]}]}`},
		{name: "trailing document", output: `{"steps":[]}{"steps":[]}`},
		{name: "trailing delimiter", output: `{"steps":[]}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			repoRoot := t.TempDir()
			scriptPath := filepath.Join(repoRoot, "planner.sh")
			writeScript(t, scriptPath, "printf '%s\\n' '"+test.output+"'\n")

			_, err := Execute(t.Context(), repoRoot, "owner/repo", "abc123", filepath.Join(repoRoot, config.DefaultPath), config.Planner{Command: []string{"/bin/sh", scriptPath}})
			if err == nil || !strings.Contains(err.Error(), "decode planner output") {
				t.Fatalf("error = %v, want a decode failure", err)
			}
		})
	}
}

// writeScript writes a planner run through /bin/sh. Executing a file that was just written
// can fail with ETXTBSY on Linux while parallel tests fork.
func writeScript(t *testing.T, path string, content string) {
	t.Helper()

	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", path, err)
	}
}

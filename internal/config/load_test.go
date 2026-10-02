package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadAppliesDefaults(t *testing.T) {
	t.Parallel()

	cfg, err := Load(writeTempConfig(t, "version = 1\n[github]\nenabled = true\n[[steps]]\nid = 'fmt'\ncommand = ['go', 'fmt', './...']\n"))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.MaxParallel != 1 || cfg.GitHub.AggregateContext != DefaultAggregateContext {
		t.Fatalf("max_parallel = %d, aggregate context = %q", cfg.MaxParallel, cfg.GitHub.AggregateContext)
	}
	// Planner plans get the same step defaults as static config.
	planner := ResolvedPlan{Steps: []Step{{ID: "fmt", Command: []string{"go", "fmt", "./..."}}}}
	planner.ApplyDefaults()
	for name, step := range map[string]Step{"static": cfg.Steps[0], "planner": planner.Steps[0]} {
		if step.Name != "fmt" || step.Dir != DefaultWorkingDir || step.GitHubContext != "local/fmt" {
			t.Fatalf("%s step defaults = %+v", name, step)
		}
	}
}

func TestLoadAcceptsParallelismAndTimeouts(t *testing.T) {
	t.Parallel()

	cfg, err := Load(writeTempConfig(t, "version = 1\nmax_parallel = 3\n[[steps]]\nid = 'test'\ncommand = ['true']\ntimeout = '10m'\n"))
	if err != nil || cfg.MaxParallel != 3 {
		t.Fatalf("max_parallel = %d, error = %v", cfg.MaxParallel, err)
	}
	if got := cfg.StaticPlan().Steps[0].Timeout; got != "10m" {
		t.Fatalf("static plan timeout = %q, want 10m", got)
	}
}

func TestLoadRejectsInvalidConfig(t *testing.T) {
	t.Parallel()

	step := func(fields string) string {
		return "version = 1\n[[steps]]\nid = 'test'\ncommand = ['true']\n" + fields + "\n"
	}
	for _, test := range []struct {
		name      string
		content   string
		wantError string
	}{
		{name: "future version", content: strings.Replace(step(""), "version = 1", "version = 2", 1), wantError: "version must be 1"},
		{name: "missing version", content: strings.Replace(step(""), "version = 1\n", "", 1), wantError: "version must be 1"},
		{name: "unknown field", content: step("bogus = 'nope'"), wantError: "unknown fields"},
		{name: "max_parallel under github", content: "version = 1\n[github]\nmax_parallel = 2\n" + strings.Replace(step(""), "version = 1\n", "", 1), wantError: "unknown fields"},
		{name: "zero max_parallel", content: strings.Replace(step(""), "version = 1", "version = 1\nmax_parallel = 0", 1), wantError: "positive integer"},
		{name: "negative max_parallel", content: strings.Replace(step(""), "version = 1", "version = 1\nmax_parallel = -1", 1), wantError: "positive integer"},
		{name: "planner and steps", content: strings.Replace(step(""), "version = 1", "version = 1\n[planner]\ncommand = ['./plan.sh']", 1), wantError: "either planner or steps"},
		{name: "neither planner nor steps", content: "version = 1\n", wantError: "at least one step"},
		{name: "empty planner command", content: "version = 1\n[planner]\ncommand = []\n", wantError: "planner.command must not be empty"},
		{name: "empty step id", content: "version = 1\n[[steps]]\nid = ''\ncommand = ['true']\n", wantError: "step id is required"},
		// Step ids become directory names under the run, so they must not escape it.
		{name: "path traversal id", content: "version = 1\n[[steps]]\nid = 'x/../../evil'\ncommand = ['true']\n", wantError: "invalid id"},
		{name: "leading dash id", content: "version = 1\n[[steps]]\nid = '-lint'\ncommand = ['true']\n", wantError: "invalid id"},
		{name: "duplicate id", content: step("") + "[[steps]]\nid = 'test'\ncommand = ['true']\n", wantError: "duplicate step id"},
		{name: "unknown need", content: step("needs = ['fmt']"), wantError: `needs unknown step "fmt"`},
		{name: "self dependency", content: step("needs = ['test']"), wantError: "cannot depend on itself"},
		{name: "duplicate dependency", content: step("") + "[[steps]]\nid = 'after'\ncommand = ['true']\nneeds = ['test', 'test']\n", wantError: "duplicate dependency"},
		{name: "dependency cycle", content: "version = 1\n[[steps]]\nid = 'a'\ncommand = ['true']\nneeds = ['b']\n[[steps]]\nid = 'b'\ncommand = ['true']\nneeds = ['a']\n", wantError: "dependency cycle detected"},
		{name: "expression condition", content: step("if = \"env.CHANGED == 'python'\""), wantError: `if must be empty, "true", or "false"`},
		{name: "empty command", content: "version = 1\n[[steps]]\nid = 'test'\ncommand = []\n", wantError: "must not be empty"},
		{name: "empty executable", content: "version = 1\n[[steps]]\nid = 'test'\ncommand = ['']\n", wantError: "non-empty executable"},
		{name: "zero timeout", content: step("timeout = '0s'"), wantError: "positive duration"},
		{name: "negative timeout", content: step("timeout = '-1s'"), wantError: "positive duration"},
		{name: "unparsable timeout", content: step("timeout = 'tomorrow'"), wantError: "positive duration"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, err := Load(writeTempConfig(t, test.content)); err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("error = %v, want %q\n%s", err, test.wantError, test.content)
			}
		})
	}
}

func writeTempConfig(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), DefaultPath)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	return path
}

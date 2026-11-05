package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFreshFailureInvalidatesAnOlderCachedPass(t *testing.T) {
	for _, breakSummary := range []bool{false, true} {
		t.Run(fmt.Sprintf("fatal=%t", breakSummary), func(t *testing.T) {
			root := newGitRepo(t)
			writeFile(t, filepath.Join(root, ".local-ci.toml"), []byte(`version = 1
[[steps]]
id = 'check'
command = ['sh', '-c', 'printf x >> "$LOCAL_CI_SOURCE_ROOT/.local-ci/executions"; if [ "$LOCAL_CI_TEST_BREAK_SUMMARY" = true ]; then rm "$LOCAL_CI_RUN_DIR/summary.json"; mkdir "$LOCAL_CI_RUN_DIR/summary.json"; fi; test "$LOCAL_CI_TEST_CHECK_OK" = yes']
`))
			runGit(t, root, "add", ".local-ci.toml")
			runGit(t, root, "commit", "-m", "external input check")
			t.Setenv("LOCAL_CI_TEST_CHECK_OK", "yes")
			t.Setenv("LOCAL_CI_TEST_BREAK_SUMMARY", "false")
			var stdout, stderr bytes.Buffer
			command := newCLI(&stdout, &stderr, root)
			if err := command.runWithContext(t.Context(), nil, []string{"run", "--no-github", "--json"}); err != nil {
				t.Fatal(err)
			}
			t.Setenv("LOCAL_CI_TEST_CHECK_OK", "no")
			for i, arguments := range [][]string{{"run", "--no-github", "--json", "--no-reuse"}, {"run", "--no-github", "--json"}} {
				t.Setenv("LOCAL_CI_TEST_BREAK_SUMMARY", fmt.Sprint(breakSummary && i == 0))
				stdout.Reset()
				stderr.Reset()
				err := command.runWithContext(t.Context(), nil, arguments)
				if breakSummary && i == 0 {
					if err == nil || err == errRunFailed || !strings.Contains(err.Error(), "summary.json") {
						t.Fatalf("expected fatal summary persistence error: %v", err)
					}
					continue
				}
				if err != errRunFailed {
					t.Fatalf("known failure reused old success: args=%v, error=%v, output=%s", arguments, err, stdout.String())
				}
				var shown showJSON
				if err := json.Unmarshal(stdout.Bytes(), &shown); err != nil {
					t.Fatal(err)
				}
				if shown.Meta.ReusedFrom != nil {
					t.Fatalf("failed run reused %+v", shown.Meta.ReusedFrom)
				}
			}
			executions, err := os.ReadFile(filepath.Join(root, ".local-ci", "executions"))
			if err != nil || string(executions) != "xxx" {
				t.Fatalf("executions=%q, error=%v", executions, err)
			}
		})
	}
}

func TestConfigLocationSeparatesCachedResults(t *testing.T) {
	t.Parallel()
	root := newGitRepo(t)
	configuration := []byte(`version = 1
[[steps]]
id = 'check'
command = ['sh', '-c', 'test -f "$(dirname "$LOCAL_CI_CONFIG")/ready"']
`)
	for _, directory := range []string{"good", "bad"} {
		if err := os.MkdirAll(filepath.Join(root, directory), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(root, "good", "ci.toml"), configuration)
	writeFile(t, filepath.Join(root, "bad", "ci.toml"), configuration)
	writeFile(t, filepath.Join(root, "good", "ready"), []byte("ready"))
	runGit(t, root, "add", "good", "bad", ".local-ci.toml")
	runGit(t, root, "commit", "-m", "two config locations")
	var stdout, stderr bytes.Buffer
	command := newCLI(&stdout, &stderr, root)
	if err := command.runWithContext(t.Context(), nil, []string{"run", "--config", "good/ci.toml", "--no-github", "--json"}); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	if err := command.runWithContext(t.Context(), nil, []string{"run", "--config", "bad/ci.toml", "--no-github", "--json"}); err != errRunFailed {
		t.Fatalf("different config location borrowed success: error=%v output=%s", err, stdout.String())
	}
	stdout.Reset()
	stderr.Reset()
	if err := command.runWithContext(t.Context(), nil, []string{"run", "--config", "good/ci.toml", "--no-github", "--json"}); err != nil {
		t.Fatal(err)
	}
	var reused showJSON
	if err := json.Unmarshal(stdout.Bytes(), &reused); err != nil {
		t.Fatal(err)
	}
	if reused.Meta.ReusedFrom == nil {
		t.Fatal("failure at a different config location discarded the good config's cached pass")
	}
}

func TestCrossCheckoutReuseCannotSkipAbsoluteDirectoryValidation(t *testing.T) {
	t.Parallel()
	sourceRoot, err := filepath.EvalSymlinks(newGitRepo(t))
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(sourceRoot, ".local-ci.toml"), []byte(fmt.Sprintf("version = 1\n[[steps]]\nid = 'check'\ndir = '%s'\ncommand = ['true']\n", sourceRoot)))
	runGit(t, sourceRoot, "add", ".local-ci.toml")
	runGit(t, sourceRoot, "commit", "-m", "absolute directory check")
	var stdout, stderr bytes.Buffer
	if err := newCLI(&stdout, &stderr, sourceRoot).runWithContext(t.Context(), nil, []string{"run", "--no-github", "--json"}); err != nil {
		t.Fatal(err)
	}
	destinationRoot := filepath.Join(t.TempDir(), "checkout")
	runGit(t, sourceRoot, "clone", sourceRoot, destinationRoot)
	runGit(t, destinationRoot, "remote", "set-url", "origin", "https://github.com/owner/repo.git")
	stdout.Reset()
	stderr.Reset()
	if err := newCLI(&stdout, &stderr, destinationRoot).runWithContext(t.Context(), nil, []string{"run", "--no-github", "--json"}); err != errRunFailed {
		t.Fatalf("cache bypassed destination directory guard: error=%v, output=%s", err, stdout.String())
	}
	if !strings.Contains(stdout.String(), "outside snapshot") {
		t.Fatalf("wrong failure: %s", stdout.String())
	}
}

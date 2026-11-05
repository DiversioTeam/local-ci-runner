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
	root := newGitRepo(t)
	writeFile(t, filepath.Join(root, ".local-ci.toml"), []byte(`version = 1
[[steps]]
id = 'check'
command = ['sh', '-c', 'printf x >> "$LOCAL_CI_SOURCE_ROOT/.local-ci/executions"; test "$LOCAL_CI_TEST_CHECK_OK" = yes']
`))
	runGit(t, root, "add", ".local-ci.toml")
	runGit(t, root, "commit", "-m", "external input check")
	t.Setenv("LOCAL_CI_TEST_CHECK_OK", "yes")
	var stdout, stderr bytes.Buffer
	command := newCLI(&stdout, &stderr, root)
	if err := command.runWithContext(t.Context(), nil, []string{"run", "--no-github", "--json"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LOCAL_CI_TEST_CHECK_OK", "no")
	for _, arguments := range [][]string{{"run", "--no-github", "--json", "--no-reuse"}, {"run", "--no-github", "--json"}} {
		stdout.Reset()
		stderr.Reset()
		if err := command.runWithContext(t.Context(), nil, arguments); err != errRunFailed {
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

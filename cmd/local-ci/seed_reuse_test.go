package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSeedWritesRemainReusableWithMatchingInitialInputs(t *testing.T) {
	t.Parallel()
	root := newGitRepo(t)
	writeFile(t, filepath.Join(root, ".gitignore"), []byte(".env\n"))
	writeFile(t, filepath.Join(root, ".env"), []byte("good"))
	writeFile(t, filepath.Join(root, ".local-ci.toml"), []byte(`version = 1
[snapshot]
seed = ['.env']
allow_plain_copy = true
[[steps]]
id = 'check'
command = ['sh', '-c', 'printf changed > .env; printf x >> "$LOCAL_CI_SOURCE_ROOT/.local-ci/executions"']
`))
	runGit(t, root, "add", ".gitignore", ".local-ci.toml")
	runGit(t, root, "commit", "-m", "seed-writing check")
	for i := 0; i < 2; i++ {
		var stdout, stderr bytes.Buffer
		if err := newCLI(&stdout, &stderr, root).runWithContext(t.Context(), nil, []string{"run", "--no-github", "--json"}); err != nil {
			t.Fatalf("run: %v, %s", err, stderr.String())
		}
		var shown showJSON
		if err := json.Unmarshal(stdout.Bytes(), &shown); err != nil {
			t.Fatal(err)
		}
		if (shown.Meta.ReusedFrom != nil) != (i == 1) || shown.Meta.SeedHash == "" || shown.Meta.TestedTreeHash == "" {
			t.Fatalf("matching initial seeds did not reuse the completed plan: %+v", shown.Meta)
		}
	}
	executions, err := os.ReadFile(filepath.Join(root, ".local-ci", "executions"))
	if err != nil || string(executions) != "x" {
		t.Fatalf("executions=%q error=%v", executions, err)
	}
	contents, err := os.ReadFile(filepath.Join(root, ".env"))
	if err != nil || string(contents) != "good" {
		t.Fatalf("snapshot changed source seeds: %q %v", contents, err)
	}
}

func TestResumeRerunsSuccessfulStepsAfterSeedsChange(t *testing.T) {
	t.Parallel()
	root := newGitRepo(t)
	writeFile(t, filepath.Join(root, ".gitignore"), []byte(".env\n"))
	writeFile(t, filepath.Join(root, ".env"), []byte("before"))
	writeFile(t, filepath.Join(root, ".local-ci.toml"), []byte(`version = 1
[snapshot]
seed = ['.env']
allow_plain_copy = true
[[steps]]
id = 'read'
command = ['sh', '-c', 'cat .env > "$LOCAL_CI_RUN_DIR/value"; printf x >> "$LOCAL_CI_SOURCE_ROOT/.local-ci/executions"']
[[steps]]
id = 'wait'
needs = ['read']
command = ['sh', '-c', 'test -f "$LOCAL_CI_SOURCE_ROOT/.local-ci/release"']
`))
	runGit(t, root, "add", ".gitignore", ".local-ci.toml")
	runGit(t, root, "commit", "-m", "resumable seeded check")
	var stdout, stderr bytes.Buffer
	command := newCLI(&stdout, &stderr, root)
	if err := command.runWithContext(t.Context(), nil, []string{"run", "--no-github", "--json"}); err != errRunFailed {
		t.Fatalf("first run: %v", err)
	}
	var first showJSON
	if err := json.Unmarshal(stdout.Bytes(), &first); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, ".env"), []byte("after"))
	writeFile(t, filepath.Join(root, ".local-ci", "release"), nil)
	stdout.Reset()
	stderr.Reset()
	if err := command.runWithContext(t.Context(), nil, []string{"resume", first.RunID, "--no-github", "--json"}); err != nil {
		t.Fatalf("resume: %v, %s", err, stderr.String())
	}
	executions, err := os.ReadFile(filepath.Join(root, ".local-ci", "executions"))
	if err != nil || string(executions) != "xx" {
		t.Fatalf("successful step retained stale seed inputs: %q %v", executions, err)
	}
	value, err := os.ReadFile(filepath.Join(first.RunDir, "value"))
	if err != nil || string(value) != "after" {
		t.Fatalf("resumed value=%q error=%v", value, err)
	}
}

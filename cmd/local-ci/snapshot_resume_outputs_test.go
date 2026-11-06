package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSnapshotResumeRebuildsIgnoredPrerequisiteOutputs(t *testing.T) {
	t.Parallel()
	root := newGitRepo(t)
	writeFile(t, filepath.Join(root, ".gitignore"), []byte(".venv/\n"))
	writeFile(t, filepath.Join(root, ".local-ci.toml"), []byte(`version = 1
[[steps]]
id = 'setup'
command = ['sh', '-c', 'mkdir .venv; touch .venv/marker; printf x >> "$LOCAL_CI_SOURCE_ROOT/.local-ci/executions"']
[[steps]]
id = 'check'
needs = ['setup']
command = ['sh', '-c', 'test ! -e .venv/marker']
`))
	runGit(t, root, "add", ".gitignore", ".local-ci.toml")
	runGit(t, root, "commit", "-m", "ignored prerequisite output")
	var stdout, stderr bytes.Buffer
	command := newCLI(&stdout, &stderr, root)
	if err := command.runWithContext(t.Context(), nil, []string{"run", "--no-github", "--json"}); err != errRunFailed {
		t.Fatalf("first run: %v", err)
	}
	var first showJSON
	if err := json.Unmarshal(stdout.Bytes(), &first); err != nil {
		t.Fatal(err)
	}
	for _, extraArguments := range [][]string{nil, {"--from-step", "check"}} {
		stdout.Reset()
		stderr.Reset()
		arguments := append([]string{"resume", first.RunID, "--no-github", "--json"}, extraArguments...)
		if err := command.runWithContext(t.Context(), nil, arguments); err != errRunFailed {
			t.Fatalf("resume falsely passed without prerequisite output: %v %s", err, stdout.String())
		}
	}
	executions, err := os.ReadFile(filepath.Join(root, ".local-ci", "executions"))
	if err != nil || string(executions) != "xxx" {
		t.Fatalf("prerequisite did not run in every fresh snapshot: %q %v", executions, err)
	}
}

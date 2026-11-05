package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOverlappingFailureInvalidatesAnInterveningPass(t *testing.T) {
	t.Parallel()
	for _, breakSummary := range []bool{false, true} {
		t.Run(fmt.Sprintf("fatal=%t", breakSummary), func(t *testing.T) {
			t.Parallel()
			sourceRoot := newGitRepo(t)
			statePath := filepath.Join(t.TempDir(), "state")
			writeFile(t, statePath, []byte("good"))
			script := fmt.Sprintf(`printf x >> "$LOCAL_CI_SOURCE_ROOT/.local-ci/executions"
if [ -f "$LOCAL_CI_SOURCE_ROOT/.local-ci/wait" ]; then
 touch "$LOCAL_CI_SOURCE_ROOT/.local-ci/started"
 while [ ! -f "$LOCAL_CI_SOURCE_ROOT/.local-ci/release" ]; do sleep 0.01; done
fi
if [ -f "$LOCAL_CI_SOURCE_ROOT/.local-ci/break-summary" ]; then
 rm "$LOCAL_CI_RUN_DIR/summary.json"
 mkdir "$LOCAL_CI_RUN_DIR/summary.json"
fi
test "$(cat %q)" = good`, statePath)
			writeFile(t, filepath.Join(sourceRoot, ".local-ci.toml"), []byte(fmt.Sprintf("version = 1\n[[steps]]\nid = 'check'\ncommand = ['sh', '-c', '''%s''']\n", script)))
			runGit(t, sourceRoot, "add", ".local-ci.toml")
			runGit(t, sourceRoot, "commit", "-m", "overlapping checks")
			otherRoot := filepath.Join(t.TempDir(), "checkout")
			runGit(t, sourceRoot, "clone", sourceRoot, otherRoot)
			runGit(t, otherRoot, "remote", "set-url", "origin", "https://github.com/owner/repo.git")
			if err := os.MkdirAll(filepath.Join(sourceRoot, ".local-ci"), 0o755); err != nil {
				t.Fatal(err)
			}
			writeFile(t, filepath.Join(sourceRoot, ".local-ci", "wait"), nil)
			if breakSummary {
				writeFile(t, filepath.Join(sourceRoot, ".local-ci", "break-summary"), nil)
			}
			commandContext, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			var sourceOutput, sourceErrors bytes.Buffer
			completed := make(chan error, 1)
			go func() {
				completed <- newCLI(&sourceOutput, &sourceErrors, sourceRoot).runWithContext(commandContext, nil, []string{"run", "--no-reuse", "--no-github", "--json"})
			}()
			for {
				if _, err := os.Stat(filepath.Join(sourceRoot, ".local-ci", "started")); err == nil {
					break
				}
				select {
				case err := <-completed:
					t.Fatalf("first run ended before barrier: %v, %s", err, sourceErrors.String())
				case <-commandContext.Done():
					t.Fatal("timed out waiting for first run")
				case <-time.After(10 * time.Millisecond):
				}
			}
			var otherOutput, otherErrors bytes.Buffer
			if err := newCLI(&otherOutput, &otherErrors, otherRoot).runWithContext(commandContext, nil, []string{"run", "--no-reuse", "--no-github", "--json"}); err != nil {
				t.Fatalf("intervening pass: %v, %s", err, otherErrors.String())
			}
			writeFile(t, statePath, []byte("bad"))
			writeFile(t, filepath.Join(sourceRoot, ".local-ci", "release"), nil)
			select {
			case err := <-completed:
				if breakSummary {
					if err == nil || !strings.Contains(err.Error(), "summary.json") {
						t.Fatalf("expected fatal persistence error: %v", err)
					}
				} else if err != errRunFailed {
					t.Fatalf("expected fresh failure: %v", err)
				}
			case <-commandContext.Done():
				t.Fatal("first run did not finish")
			}
			if breakSummary {
				if err := os.Remove(filepath.Join(sourceRoot, ".local-ci", "break-summary")); err != nil {
					t.Fatal(err)
				}
			}
			sourceOutput.Reset()
			sourceErrors.Reset()
			if err := newCLI(&sourceOutput, &sourceErrors, sourceRoot).runWithContext(commandContext, nil, []string{"run", "--no-github", "--json"}); err != errRunFailed {
				t.Fatalf("retry borrowed intervening pass: %v, %s", err, sourceOutput.String())
			}
			executions, err := os.ReadFile(filepath.Join(sourceRoot, ".local-ci", "executions"))
			if err != nil || string(executions) != "xx" {
				t.Fatalf("retry did not execute: %q, %v", executions, err)
			}
		})
	}
}

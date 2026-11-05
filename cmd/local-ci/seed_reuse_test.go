package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
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

func TestSeedHardLinksDoNotReuseIndependentFileSuccess(t *testing.T) {
	t.Parallel()
	for _, splitSeeds := range []bool{false, true} {
		name := "directory"
		if splitSeeds {
			name = "separate seeds"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if !splitSeeds && runtime.GOOS != "linux" {
				t.Skip("Linux cp -a preserves aliases inside a directory seed")
			}
			root := newGitRepo(t)
			a, b := filepath.Join(root, "cache", "a"), filepath.Join(root, "cache", "b")
			if err := os.Mkdir(filepath.Dir(a), 0o755); err != nil {
				t.Fatal(err)
			}
			writeFile(t, a, []byte("original"))
			writeFile(t, b, []byte("original"))
			// Normal pnpm store links outside the seeds must still permit cache hits.
			storeFile := filepath.Join(t.TempDir(), "store-file")
			if err := os.Link(a, storeFile); err != nil {
				t.Fatal(err)
			}
			seeds := "['cache']"
			if splitSeeds {
				seeds = "['cache/a', 'cache/b']"
			}
			writeFile(t, filepath.Join(root, ".gitignore"), []byte("cache/\n"))
			writeFile(t, filepath.Join(root, ".local-ci.toml"), []byte(`version = 1
[snapshot]
seed = `+seeds+`
allow_plain_copy = true
[[steps]]
id = 'check'
command = ['sh', '-c', 'printf x >> "$LOCAL_CI_SOURCE_ROOT/.local-ci/executions"; printf changed > cache/a; test "$(cat cache/b)" = original']
`))
			runGit(t, root, "add", ".gitignore", ".local-ci.toml")
			runGit(t, root, "commit", "-m", "hard-link-sensitive check")
			run := func(noReuse, wantReuse, wantFailure, wantHash bool) {
				t.Helper()
				var stdout, stderr bytes.Buffer
				args := []string{"run", "--no-github", "--json"}
				if noReuse {
					args = append(args, "--no-reuse")
				}
				err := newCLI(&stdout, &stderr, root).runWithContext(t.Context(), nil, args)
				if (wantFailure && err != errRunFailed) || (!wantFailure && err != nil) {
					t.Fatalf("run(noReuse=%t): %v, wantFailure=%t; %s", noReuse, err, wantFailure, stderr.String())
				}
				var shown showJSON
				if err := json.Unmarshal(stdout.Bytes(), &shown); err != nil {
					t.Fatal(err)
				}
				if (shown.Meta.ReusedFrom != nil) != wantReuse || (shown.Meta.SeedHash != "") != wantHash || shown.Meta.TestedTreeHash == "" {
					t.Fatalf("unexpected seed reuse identity: %+v", shown.Meta)
				}
			}
			run(false, false, false, true)
			run(false, true, false, true)
			run(true, false, false, true)
			if err := os.Remove(b); err != nil {
				t.Fatal(err)
			}
			if err := os.Link(a, b); err != nil {
				t.Fatal(err)
			}
			// Separate copy calls break aliases: those valid snapshots must still execute.
			run(false, false, !splitSeeds, false)
			run(true, false, !splitSeeds, false)
			if executions, err := os.ReadFile(filepath.Join(root, ".local-ci", "executions")); err != nil || string(executions) != "xxxx" {
				t.Fatalf("cache-hit versus fresh executions=%q error=%v", executions, err)
			}
			for _, path := range []string{a, b, storeFile} {
				if contents, err := os.ReadFile(path); err != nil || string(contents) != "original" {
					t.Fatalf("snapshot modified live seed/store %s: %q, %v", path, contents, err)
				}
			}
		})
	}
}

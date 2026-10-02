package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DiversioTeam/local-ci-runner/internal/config"
	"github.com/DiversioTeam/local-ci-runner/internal/persistence"
)

func TestPlanAndRunJSONShareSnapshotAndDoNotMixProcessOutput(t *testing.T) {
	t.Parallel()
	root := newGitRepo(t)
	store := persistence.NewStore(root)
	writeFile(t, filepath.Join(root, config.DefaultPath), []byte(`version = 1
max_parallel = 2
[[steps]]
id = "first"
command = ["sh", "-c", "touch .local-ci/check-executed; printf noisy-child-output; printf noisy-child-error >&2"]
timeout = "5s"
[[steps]]
id = "second"
command = ["sh", "-c", "printf second >> \"$LOCAL_CI_STEP_DIR/execution-count\""]
needs = ["first"]
`))
	var stdout, stderr bytes.Buffer
	command := newCLI(&stdout, &stderr, root)
	if err := command.run([]string{"plan", "--json"}); err != nil {
		t.Fatal(err)
	}
	var planned planJSON
	if err := json.Unmarshal(stdout.Bytes(), &planned); err != nil {
		t.Fatal(err)
	}
	if planned.MaxParallel != 2 || len(planned.Plan.Steps) != 2 || planned.Plan.Steps[0].Timeout != "5s" {
		t.Fatalf("plan = %+v", planned)
	}
	if runIDs, err := store.ListRunIDs(); err != nil || len(runIDs) != 0 {
		t.Fatalf("plan created run artifacts: %v, error = %v", runIDs, err)
	}
	if _, err := os.Stat(filepath.Join(root, ".local-ci", "check-executed")); !os.IsNotExist(err) {
		t.Fatal("plan executed a step")
	}
	stdout.Reset()
	if err := command.run([]string{"run", "--json", "--no-github"}); err != nil {
		t.Fatal(err)
	}
	var completed showJSON
	if err := json.Unmarshal(stdout.Bytes(), &completed); err != nil {
		t.Fatalf("run stdout is not one JSON object: %v\n%s", err, stdout.String())
	}
	if completed.Status != "success" || completed.Meta.PlanHash != planned.PlanHash || completed.Meta.MaxParallel != 2 {
		t.Fatalf("completed = %+v", completed.Meta)
	}
	if _, err := os.Stat(filepath.Join(root, ".local-ci", "check-executed")); err != nil {
		t.Fatalf("run did not execute the check: %v", err)
	}
	stdout.Reset()
	if err := command.run([]string{"resume", "--max-parallel", "1", completed.RunID, "--from-step=first", "--json"}); err != nil {
		t.Fatal(err)
	}
	var resumed showJSON
	if err := json.Unmarshal(stdout.Bytes(), &resumed); err != nil {
		t.Fatal(err)
	}
	if resumed.RunID != completed.RunID || resumed.Meta.MaxParallel != 1 {
		t.Fatalf("resumed = %+v", resumed.Meta)
	}
	count, err := os.ReadFile(filepath.Join(completed.RunDir, filepath.Dir(completed.Steps[1].CombinedLog), "execution-count"))
	if err != nil || string(count) != "secondsecond" {
		t.Fatalf("execution count = %s, error = %v", count, err)
	}
}

func TestPlannerCannotChangeTheCapturedSnapshotOrConfiguration(t *testing.T) {
	t.Parallel()
	const (
		snapshotChanged = "planner changed repository identity"
		configChanged   = "configuration changed while preparing"
	)
	for _, testCase := range []struct {
		name      string
		command   string
		wantError string
	}{
		{name: "tracked source", command: "printf changed >> README.md", wantError: snapshotChanged},
		{name: "HEAD", command: "git commit --allow-empty -m planner >&2", wantError: snapshotChanged},
		{name: "external config", command: "printf '\\n# changed\\n' >> \"$LOCAL_CI_CONFIG\"", wantError: configChanged},
		{name: "ignored output", command: "mkdir -p .local-ci && printf generated > .local-ci/generated"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			root := newGitRepo(t)
			store := persistence.NewStore(root)
			configurationPath := filepath.Join(t.TempDir(), config.DefaultPath)
			writeFile(t, configurationPath, []byte("version = 1\n[planner]\ncommand = ['sh', 'planner.sh']\n"))
			plannerSource := "#!/bin/sh\nset -e\n" + testCase.command + "\necho planner-diagnostic >&2\nprintf '%s\\n' '{\"steps\":[{\"id\":\"test\",\"command\":[\"sh\",\"-c\",\"touch .local-ci/check-ran\"]}]}'\n"
			writeFile(t, filepath.Join(root, "planner.sh"), []byte(plannerSource))
			for _, commandName := range []string{"plan", "run"} {
				var stdout, stderr bytes.Buffer
				args := []string{commandName, "--config", configurationPath, "--json"}
				if commandName == "run" {
					args = append(args, "--no-github")
				}
				err := newCLI(&stdout, &stderr, root).run(args)
				if testCase.wantError == "" {
					if err != nil {
						t.Fatalf("%s refused an ignored planner output: %v", commandName, err)
					}
					// Planner diagnostics go to stderr so stdout stays one JSON document.
					if commandName == "plan" && (!json.Valid(stdout.Bytes()) || !strings.Contains(stderr.String(), "planner-diagnostic")) {
						t.Fatalf("plan stdout = %q, stderr = %q", stdout.String(), stderr.String())
					}
					continue
				}
				if err == nil || !strings.Contains(err.Error(), testCase.wantError) {
					t.Fatalf("%s error = %v, want %q", commandName, err, testCase.wantError)
				}
				if runIDs, err := store.ListRunIDs(); err != nil || len(runIDs) != 0 {
					t.Fatalf("%s created artifacts after the planner changed inputs: %v", commandName, runIDs)
				}
				if _, err := os.Stat(filepath.Join(root, ".local-ci", "check-ran")); !os.IsNotExist(err) {
					t.Fatal("verification ran after the planner changed inputs")
				}
			}
			if _, err := os.Stat(filepath.Join(root, ".local-ci", "check-ran")); testCase.wantError == "" && err != nil {
				t.Fatal("run did not execute the plan after an ignored planner output")
			}
		})
	}
}

func TestFailedRunStillEmitsInspectableJSON(t *testing.T) {
	t.Parallel()
	root := newGitRepo(t)
	writeFile(t, filepath.Join(root, config.DefaultPath), []byte("version = 1\n[[steps]]\nid = 'fail'\ncommand = ['sh', '-c', 'echo noise; exit 7']\n"))
	var stdout, stderr bytes.Buffer
	err := newCLI(&stdout, &stderr, root).run([]string{"run", "--json", "--no-github"})
	if !errors.Is(err, errRunFailed) {
		t.Fatalf("error = %v", err)
	}
	var result showJSON
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != "failure" || result.Steps[0].ExitCode == nil || *result.Steps[0].ExitCode != 7 || result.Steps[0].CombinedLog == "" {
		t.Fatalf("result = %+v", result)
	}
}

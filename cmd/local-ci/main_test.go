package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/DiversioTeam/local-ci-runner/internal/config"
	"github.com/DiversioTeam/local-ci-runner/internal/engine"
	"github.com/DiversioTeam/local-ci-runner/internal/events"
	"github.com/DiversioTeam/local-ci-runner/internal/gitrepo"
	"github.com/DiversioTeam/local-ci-runner/internal/persistence"
)

// TestMain keeps tests away from the developer's machine: git ignores global config, and a
// gh that always fails shadows the real one, so a broken guard can never post a real status.
func TestMain(m *testing.M) {
	os.Exit(runIsolated(m))
}

func runIsolated(m *testing.M) int {
	fakeBin, err := os.MkdirTemp("", "local-ci-fake-gh-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(fakeBin)
	if err := os.WriteFile(filepath.Join(fakeBin, "gh"), []byte("#!/bin/sh\necho 'fake gh: tests must not contact GitHub' >&2\nexit 1\n"), 0o755); err != nil {
		panic(err)
	}
	os.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	os.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	os.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	return m.Run()
}

type cliFixture struct {
	root        string
	store       persistence.Store
	finishedRun engine.RunRecord
	activeRun   engine.RunRecord
}

func TestHelpSurfaces(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		args  []string
		want  string
		flags []string
	}{
		{name: "top level", args: nil, want: "Main commands:"},
		{name: "top level flag", args: []string{"--help"}, want: "Main commands:"},
		{name: "run", args: []string{"run", "--help"}, want: "Usage:\n  local-ci run", flags: []string{"--json", "--max-parallel", "--no-github"}},
		{name: "resume", args: []string{"resume", "--help"}, want: "Usage:\n  local-ci resume", flags: []string{"--json", "--max-parallel", "--from-step"}},
		{name: "plan", args: []string{"plan", "--help"}, want: "Usage:\n  local-ci plan", flags: []string{"--json", "--max-parallel"}},
		{name: "plan alias", args: []string{"help", "plan"}, want: "Usage:\n  local-ci plan"},
		{name: "runs", args: []string{"runs", "--help"}, want: "Usage:\n  local-ci runs"},
		{name: "show", args: []string{"show", "--help"}, want: "Usage:\n  local-ci show"},
		{name: "publish", args: []string{"publish", "--help"}, want: "Usage:\n  local-ci publish <run-id>"},
		{name: "version", args: []string{"version", "--help"}, want: "Usage:\n  local-ci version"},
		{name: "logs", args: []string{"logs", "--help"}, want: "Usage:\n  local-ci logs"},
		{name: "update", args: []string{"update", "--help"}, want: "Usage:\n  local-ci update"},
		{name: "help alias", args: []string{"help", "logs"}, want: "--step <id>"},
		{name: "manual", args: []string{"manual"}, want: manualText},
		{name: "manual help", args: []string{"manual", "--help"}, want: manualText},
		{name: "help all", args: []string{"help", "all"}, want: manualText},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			stdout := &bytes.Buffer{}
			stderr := &bytes.Buffer{}
			if err := newCLI(stdout, stderr, t.TempDir()).runWithContext(t.Context(), nil, testCase.args); err != nil {
				t.Fatalf("run() error = %v", err)
			}
			if got := stdout.String(); !strings.Contains(got, testCase.want) {
				t.Fatalf("stdout = %q, want substring %q", got, testCase.want)
			}
			for _, flag := range testCase.flags {
				if !strings.Contains(stdout.String(), flag) {
					t.Fatalf("help does not document %s", flag)
				}
			}
		})
	}
}

func TestParseExecutionArgs(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		args      []string
		want      executionCLIOptions
		wantError string
	}{
		{args: []string{"--config", "alt.toml", "--no-github"}, want: executionCLIOptions{configPath: "alt.toml", noGitHub: true}},
		{args: []string{"--max-parallel=2", "--from-step", "lint", "--json"}, want: executionCLIOptions{configPath: config.DefaultPath, maxParallel: 2, fromStep: "lint", json: true}},
		{args: []string{"--max-parallel"}, wantError: "positive integer"},
		{args: []string{"--max-parallel", "0"}, wantError: "positive integer"},
		{args: []string{"--max-parallel=-1"}, wantError: "positive integer"},
		{args: []string{"--max-parallel=auto"}, wantError: "positive integer"},
		{args: []string{"--from-step="}, wantError: "--from-step"},
	} {
		t.Run(strings.Join(test.args, " "), func(t *testing.T) {
			got, _, err := parseExecutionArgs(test.args, false)
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("error = %v, want %q", err, test.wantError)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("options = %+v, error = %v; want %+v", got, err, test.want)
			}
		})
	}
}

func TestUsageErrors(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		args      []string
		wantError string
	}{
		{args: []string{"bogus"}, wantError: "unknown command"},
		{args: []string{"manual", "foo", "--help"}, wantError: "manual accepts no arguments"},
		{args: []string{"logs", "run-1", "--runner", "--planner"}, wantError: "choose exactly one log source"},
		{args: []string{"logs", "run-1", "--step", "--stderr"}, wantError: "--step requires a value"},
		{args: []string{"run", "--from-step", "lint"}, wantError: "supported only by resume"},
		{args: []string{"plan", "--no-github"}, wantError: "plan accepts"},
		{args: []string{"plan", "--from-step", "lint"}, wantError: "plan accepts"},
		// A flag's value must never be mistaken for the run id.
		{args: []string{"resume", "--max-parallel", "2"}, wantError: "resume requires exactly one run id"},
		{args: []string{"resume", "run-1", "run-2"}, wantError: "resume requires exactly one run id"},
	} {
		t.Run(strings.Join(test.args, " "), func(t *testing.T) {
			t.Parallel()
			err := newCLI(&bytes.Buffer{}, &bytes.Buffer{}, t.TempDir()).runWithContext(t.Context(), nil, test.args)
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("error = %v, want %q", err, test.wantError)
			}
		})
	}
}

func TestSuppressedGitHubPostingReason(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name          string
		existing      string
		dirty         bool
		noGitHub      bool
		githubEnabled bool
		want          string
	}{
		{name: "github disabled", githubEnabled: false, want: ""},
		{name: "cli disabled", githubEnabled: true, noGitHub: true, want: "cli_disabled"},
		{name: "existing persists", githubEnabled: true, existing: "cli_disabled", want: "cli_disabled"},
		{name: "post failure persists", githubEnabled: true, existing: persistence.GitHubPostingSuppressionPostFailed, want: persistence.GitHubPostingSuppressionPostFailed},
		{name: "dirty worktree", githubEnabled: true, dirty: true, want: "dirty_worktree"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := suppressedGitHubPostingReason(testCase.existing, testCase.dirty, testCase.noGitHub, testCase.githubEnabled); got != testCase.want {
				t.Fatalf("suppressedGitHubPostingReason() = %q, want %q", got, testCase.want)
			}
		})
	}
}

func TestPublishRefusesRunForChangedCheckout(t *testing.T) {
	t.Parallel()

	root := newGitRepo(t)
	writeFile(t, filepath.Join(root, config.DefaultPath), []byte("version = 1\n[github]\nenabled = true\n[[steps]]\nid = 'check'\ncommand = ['sh', '-c', 'exit 0']\n"))
	runGit(t, root, "add", config.DefaultPath)
	runGit(t, root, "commit", "-m", "config")
	var stdout bytes.Buffer
	if err := newCLI(&stdout, &bytes.Buffer{}, root).runWithContext(t.Context(), nil, []string{"run", "--no-github", "--json"}); err != nil {
		t.Fatal(err)
	}
	var completed showJSON
	if err := json.Unmarshal(stdout.Bytes(), &completed); err != nil {
		t.Fatal(err)
	}
	// A change after the run means the stored result no longer describes this checkout.
	writeFile(t, filepath.Join(root, "README.md"), []byte("changed after the run\n"))
	err := newCLI(&bytes.Buffer{}, &bytes.Buffer{}, root).runWithContext(t.Context(), nil, []string{"publish", completed.RunID})
	if err == nil || !strings.Contains(err.Error(), "current worktree is dirty") {
		t.Fatalf("publish error = %v, want the dirty-worktree refusal", err)
	}
}

func TestValidatePublishableRunRefusesChangedIdentityOrSnapshot(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name      string
		change    func(*gitrepo.Info, *engine.RunIdentity, *engine.RunRecord)
		wantError string
	}{
		{name: "unchanged", change: func(*gitrepo.Info, *engine.RunIdentity, *engine.RunRecord) {}},
		{name: "repo root", change: func(repo *gitrepo.Info, _ *engine.RunIdentity, _ *engine.RunRecord) { repo.Root = "/other" }, wantError: "repo root changed"},
		{name: "repo slug", change: func(repo *gitrepo.Info, _ *engine.RunIdentity, _ *engine.RunRecord) { repo.RepoSlug = "other/repo" }, wantError: "repo slug changed"},
		{name: "config path", change: func(_ *gitrepo.Info, identity *engine.RunIdentity, _ *engine.RunRecord) {
			identity.ConfigPath = "/repo/other.toml"
		}, wantError: "config path changed"},
		{name: "config hash", change: func(_ *gitrepo.Info, identity *engine.RunIdentity, _ *engine.RunRecord) {
			identity.ConfigHash = "cfg-2"
		}, wantError: "config hash changed"},
		{name: "plan hash", change: func(_ *gitrepo.Info, identity *engine.RunIdentity, _ *engine.RunRecord) { identity.PlanHash = "plan-2" }, wantError: "plan hash changed"},
		{name: "dirty worktree", change: func(repo *gitrepo.Info, _ *engine.RunIdentity, _ *engine.RunRecord) { repo.DirtyWorktree = true }, wantError: "current worktree is dirty"},
		{name: "no stored snapshot", change: func(_ *gitrepo.Info, _ *engine.RunIdentity, run *engine.RunRecord) { run.Meta.WorktreeTreeHash = "" }, wantError: "does not record a worktree snapshot"},
		{name: "HEAD differs from worktree", change: func(repo *gitrepo.Info, _ *engine.RunIdentity, _ *engine.RunRecord) { repo.WorktreeTreeHash = "tree-2" }, wantError: "does not match current worktree"},
		{
			name: "HEAD differs from the tree that ran",
			change: func(repo *gitrepo.Info, _ *engine.RunIdentity, _ *engine.RunRecord) {
				repo.HeadTreeHash, repo.WorktreeTreeHash = "tree-2", "tree-2"
			},
			wantError: "does not match stored run snapshot",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			// A suppressed, finished run whose identity matches the current clean checkout.
			repo := gitrepo.Info{Root: "/repo", RepoSlug: "owner/repo", HeadSHA: "def456", HeadTreeHash: "tree-1", WorktreeTreeHash: "tree-1"}
			identity := engine.RunIdentity{RepoRoot: "/repo", RepoSlug: "owner/repo", HeadSHA: "def456", ConfigPath: "/repo/.local-ci.toml", ConfigHash: "cfg-1", PlanHash: "plan-1", WorktreeTreeHash: "tree-1"}
			run := engine.RunRecord{
				RunID: "run-1",
				Meta: persistence.Meta{
					RepoRoot: "/repo", RepoSlug: "owner/repo", ConfigPath: "/repo/.local-ci.toml", ConfigHash: "cfg-1", PlanHash: "plan-1",
					GitHubEnabled: true, GitHubPostingSuppressed: "dirty_worktree", WorktreeTreeHash: "tree-1",
					FinishedAt: timePtr(time.Date(2026, 6, 27, 15, 1, 0, 0, time.UTC)),
				},
				Summary: persistence.Summary{Status: string(engine.StepStateSuccess)},
			}
			test.change(&repo, &identity, &run)
			err := validatePublishableRun(repo, identity, run)
			if test.wantError == "" {
				if err != nil {
					t.Fatalf("publishable run refused: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("error = %v, want %q", err, test.wantError)
			}
		})
	}
}

func TestRunsListsNewestFirstAndMarksActiveRun(t *testing.T) {
	t.Parallel()

	fixture := newCLIFixture(t)
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	if err := newCLI(stdout, stderr, fixture.root).runWithContext(t.Context(), nil, []string{"runs"}); err != nil {
		t.Fatalf("runs error = %v", err)
	}

	output := stdout.String()
	firstIndex := strings.Index(output, fixture.activeRun.RunID)
	secondIndex := strings.Index(output, fixture.finishedRun.RunID)
	if firstIndex < 0 || secondIndex < 0 {
		t.Fatalf("runs output missing run ids:\n%s", output)
	}
	if firstIndex >= secondIndex {
		t.Fatalf("runs output not newest first:\n%s", output)
	}
	if !strings.Contains(output, "running") {
		t.Fatalf("runs output missing running status:\n%s", output)
	}
	if !strings.Contains(output, fmt.Sprintf("%d", os.Getpid())) {
		t.Fatalf("runs output missing active runner pid:\n%s", output)
	}
	if !strings.Contains(output, "failure") {
		t.Fatalf("runs output missing failure status:\n%s", output)
	}
}

func TestShowWorksForActiveAndFinishedRuns(t *testing.T) {
	t.Parallel()

	fixture := newCLIFixture(t)

	activeOut := &bytes.Buffer{}
	if err := newCLI(activeOut, &bytes.Buffer{}, fixture.root).runWithContext(t.Context(), nil, []string{"show", fixture.activeRun.RunID}); err != nil {
		t.Fatalf("show active error = %v", err)
	}
	activeText := activeOut.String()
	if !strings.Contains(activeText, "status: running") {
		t.Fatalf("active show missing running status:\n%s", activeText)
	}
	if !strings.Contains(activeText, fmt.Sprintf("pid: %d", os.Getpid())) {
		t.Fatalf("active show missing runner pid:\n%s", activeText)
	}
	if !strings.Contains(activeText, "head_tree: head-tree") || !strings.Contains(activeText, "worktree_tree: worktree-tree") {
		t.Fatalf("active show missing snapshot hashes:\n%s", activeText)
	}
	if !strings.Contains(activeText, "github_posting_at_execution: suppressed (dirty_worktree)") {
		t.Fatalf("active show missing github suppression reason:\n%s", activeText)
	}
	if !strings.Contains(activeText, "[modified] README.md @ blob-123") {
		t.Fatalf("active show missing dirty file details:\n%s", activeText)
	}
	if !strings.Contains(activeText, filepath.Join(fixture.activeRun.RunDir, persistence.EventsFile)) {
		t.Fatalf("active show missing runner log path:\n%s", activeText)
	}
	if !strings.Contains(activeText, filepath.Join(fixture.activeRun.RunDir, persistence.StepRelPath(0, "checks-fast", persistence.CombinedLog))) {
		t.Fatalf("active show missing active step log path:\n%s", activeText)
	}

	finishedOut := &bytes.Buffer{}
	if err := newCLI(finishedOut, &bytes.Buffer{}, fixture.root).runWithContext(t.Context(), nil, []string{"show", fixture.finishedRun.RunID}); err != nil {
		t.Fatalf("show finished error = %v", err)
	}
	finishedText := finishedOut.String()
	if !strings.Contains(finishedText, "status: failure") {
		t.Fatalf("finished show missing failure status:\n%s", finishedText)
	}
	if !strings.Contains(finishedText, "failure_points:") {
		t.Fatalf("finished show missing failure section:\n%s", finishedText)
	}
	if !strings.Contains(finishedText, filepath.Join(fixture.finishedRun.RunDir, persistence.StepRelPath(1, "fail", persistence.CombinedLog))) {
		t.Fatalf("finished show missing failing combined log path:\n%s", finishedText)
	}
}

func TestInspectionFlagsDeadRunner(t *testing.T) {
	fixture := newCLIFixture(t)

	finishedProcess := exec.Command("/bin/sh", "-c", "exit 0")
	if err := finishedProcess.Start(); err != nil {
		t.Fatalf("start process: %v", err)
	}
	deadProcessID := finishedProcess.Process.Pid
	if err := finishedProcess.Wait(); err != nil {
		t.Fatalf("wait for process: %v", err)
	}
	if runnerAlive, known := getProcessAlive(deadProcessID); !known {
		if runtime.GOOS == "darwin" || runtime.GOOS == "linux" {
			t.Fatal("process liveness must be known on darwin and linux")
		}
		t.Skip("process liveness is unavailable on this platform")
	} else if runnerAlive {
		t.Fatalf("finished process %d is still reported alive", deadProcessID)
	}

	meta, err := persistence.ReadJSONFile[persistence.Meta](fixture.store.RunFile(fixture.activeRun.RunID, persistence.MetaFile))
	if err != nil {
		t.Fatalf("ReadJSONFile(meta) error = %v", err)
	}
	meta.RunnerPID = &deadProcessID
	metaPath := fixture.store.RunFile(fixture.activeRun.RunID, persistence.MetaFile)
	if err := persistence.WriteJSONFile(metaPath, meta); err != nil {
		t.Fatalf("WriteJSONFile(meta) error = %v", err)
	}
	storedMeta, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// A dead runner is reported, never repaired, by read commands.
		if after, err := os.ReadFile(metaPath); err != nil || !bytes.Equal(storedMeta, after) {
			t.Error("inspection rewrote meta.json")
		}
	})

	runsOutput := &bytes.Buffer{}
	if err := newCLI(runsOutput, &bytes.Buffer{}, fixture.root).runWithContext(t.Context(), nil, []string{"runs"}); err != nil {
		t.Fatalf("runs error = %v", err)
	}
	if got := runsOutput.String(); !strings.Contains(got, "running (dead)") {
		t.Fatalf("runs output = %q, want dead runner", got)
	}

	showOutput := &bytes.Buffer{}
	if err := newCLI(showOutput, &bytes.Buffer{}, fixture.root).runWithContext(t.Context(), nil, []string{"show", fixture.activeRun.RunID}); err != nil {
		t.Fatalf("show error = %v", err)
	}
	if got := showOutput.String(); !strings.Contains(got, fmt.Sprintf("pid: %d (dead)", deadProcessID)) {
		t.Fatalf("show output = %q, want dead runner", got)
	}

	jsonOutput := &bytes.Buffer{}
	if err := newCLI(jsonOutput, &bytes.Buffer{}, fixture.root).runWithContext(t.Context(), nil, []string{"show", fixture.activeRun.RunID, "--json"}); err != nil {
		t.Fatalf("show --json error = %v", err)
	}
	var payload showJSON
	if err := json.Unmarshal(jsonOutput.Bytes(), &payload); err != nil {
		t.Fatalf("decode show json: %v", err)
	}
	if payload.RunnerAlive == nil || *payload.RunnerAlive {
		t.Fatalf("runner_alive = %v, want false", payload.RunnerAlive)
	}
}

func TestLogsDefaultsToRunnerAndSupportsStepViews(t *testing.T) {
	t.Parallel()

	fixture := newCLIFixture(t)

	runnerOut := &bytes.Buffer{}
	if err := newCLI(runnerOut, &bytes.Buffer{}, fixture.root).runWithContext(t.Context(), nil, []string{"logs", fixture.activeRun.RunID}); err != nil {
		t.Fatalf("runner logs error = %v", err)
	}
	if got := runnerOut.String(); !strings.Contains(got, "run started") || !strings.Contains(got, "start checks-fast") {
		t.Fatalf("runner logs output = %q", got)
	}

	combinedOut := &bytes.Buffer{}
	if err := newCLI(combinedOut, &bytes.Buffer{}, fixture.root).runWithContext(t.Context(), nil, []string{"logs", fixture.activeRun.RunID, "--step", "checks-fast"}); err != nil {
		t.Fatalf("combined step logs error = %v", err)
	}
	if got := combinedOut.String(); !strings.Contains(got, "combined output") {
		t.Fatalf("combined logs output = %q", got)
	}

	stderrOut := &bytes.Buffer{}
	if err := newCLI(stderrOut, &bytes.Buffer{}, fixture.root).runWithContext(t.Context(), nil, []string{"logs", fixture.activeRun.RunID, "--step", "checks-fast", "--stderr"}); err != nil {
		t.Fatalf("stderr step logs error = %v", err)
	}
	if got := stderrOut.String(); !strings.Contains(got, "stderr output") {
		t.Fatalf("stderr logs output = %q", got)
	}
}

func TestInspectionToleratesStatusAheadOfSummary(t *testing.T) {
	t.Parallel()

	fixture := newCLIFixture(t)
	// The writer saves a step's status before the summary, so a reader can land between the two.
	advanceStatusPastSummary(t, fixture)
	summaryPath := fixture.store.RunFile(fixture.activeRun.RunID, persistence.SummaryFile)
	before, err := os.ReadFile(summaryPath)
	if err != nil {
		t.Fatal(err)
	}

	showOutput := &bytes.Buffer{}
	if err := newCLI(showOutput, &bytes.Buffer{}, fixture.root).runWithContext(t.Context(), nil, []string{"show", fixture.activeRun.RunID}); err != nil {
		t.Fatalf("show error = %v", err)
	}
	if got := showOutput.String(); !strings.Contains(got, "success") || !strings.Contains(got, "checks-fast") {
		t.Fatalf("show output = %q, want the newer step status", got)
	}
	runsOutput := &bytes.Buffer{}
	if err := newCLI(runsOutput, &bytes.Buffer{}, fixture.root).runWithContext(t.Context(), nil, []string{"runs"}); err != nil {
		t.Fatalf("runs error = %v", err)
	}
	// runs lists every run, but a run it cannot load shows as an error row.
	for _, line := range strings.Split(runsOutput.String(), "\n") {
		if strings.HasPrefix(line, fixture.activeRun.RunID) && strings.Contains(line, "error") {
			t.Fatalf("runs could not load the active run: %q", line)
		}
	}
	if !strings.Contains(runsOutput.String(), fixture.activeRun.RunID) {
		t.Fatalf("runs output = %q, want the active run", runsOutput.String())
	}
	if after, err := os.ReadFile(summaryPath); err != nil || !bytes.Equal(before, after) {
		t.Fatal("inspection rewrote the stored summary")
	}
}

func TestJSONModes(t *testing.T) {
	t.Parallel()

	fixture := newCLIFixture(t)

	runsOut := &bytes.Buffer{}
	if err := newCLI(runsOut, &bytes.Buffer{}, fixture.root).runWithContext(t.Context(), nil, []string{"runs", "--json"}); err != nil {
		t.Fatalf("runs --json error = %v", err)
	}
	var runEntries []runListEntry
	if err := json.Unmarshal(runsOut.Bytes(), &runEntries); err != nil {
		t.Fatalf("decode runs json: %v", err)
	}
	if len(runEntries) != 2 {
		t.Fatalf("run entries = %d, want 2", len(runEntries))
	}

	showOut := &bytes.Buffer{}
	if err := newCLI(showOut, &bytes.Buffer{}, fixture.root).runWithContext(t.Context(), nil, []string{"show", fixture.activeRun.RunID, "--json"}); err != nil {
		t.Fatalf("show --json error = %v", err)
	}
	var showPayload showJSON
	if err := json.Unmarshal(showOut.Bytes(), &showPayload); err != nil {
		t.Fatalf("decode show json: %v", err)
	}
	if got, want := showPayload.Status, string(engine.StepStateRunning); got != want {
		t.Fatalf("show status = %q, want %q", got, want)
	}
	if _, known := getProcessAlive(os.Getpid()); known {
		if showPayload.RunnerAlive == nil || !*showPayload.RunnerAlive {
			t.Fatalf("runner_alive = %v, want true", showPayload.RunnerAlive)
		}
	}

	logsOut := &bytes.Buffer{}
	if err := newCLI(logsOut, &bytes.Buffer{}, fixture.root).runWithContext(t.Context(), nil, []string{"logs", fixture.activeRun.RunID, "--json"}); err != nil {
		t.Fatalf("logs --json error = %v", err)
	}
	var logsPayload logsJSON
	if err := json.Unmarshal(logsOut.Bytes(), &logsPayload); err != nil {
		t.Fatalf("decode logs json: %v", err)
	}
	if got, want := logsPayload.Source, "runner"; got != want {
		t.Fatalf("logs source = %q, want %q", got, want)
	}
	if len(logsPayload.Events) == 0 {
		t.Fatal("expected runner events")
	}
}

// newGitRepo returns a committed GitHub-remote repository with a static config.
func newGitRepo(t *testing.T) string {
	t.Helper()

	repoRoot := t.TempDir()
	runGit(t, repoRoot, "init")
	runGit(t, repoRoot, "config", "user.email", "local-ci@example.com")
	runGit(t, repoRoot, "config", "user.name", "Local CI")
	writeFile(t, filepath.Join(repoRoot, "README.md"), []byte("hello\n"))
	runGit(t, repoRoot, "add", "README.md")
	runGit(t, repoRoot, "commit", "-m", "init")
	runGit(t, repoRoot, "remote", "add", "origin", "git@github.com:owner/repo.git")
	writeFile(t, filepath.Join(repoRoot, config.DefaultPath), []byte("version = 1\n"))
	return repoRoot
}

// newCLIFixture adds a finished run and an active run for inspection commands.
func newCLIFixture(t *testing.T) cliFixture {
	t.Helper()

	repoRoot := newGitRepo(t)
	store := persistence.NewStore(repoRoot)
	headSHA := gitOutput(t, repoRoot, "rev-parse", "HEAD")
	configPath := filepath.Join(repoRoot, config.DefaultPath)

	finishedPlan := config.ResolvedPlan{Steps: []config.Step{
		{ID: "prep", Command: []string{"/bin/sh", "-c", "printf 'prep\\n'"}},
		{ID: "fail", Command: []string{"/bin/sh", "-c", "printf 'boom\\n' >&2; exit 3"}, Needs: []string{"prep"}},
		{ID: "after", Command: []string{"/bin/sh", "-c", "printf 'after\\n'"}, Needs: []string{"fail"}},
	}}
	finishedPlan.ApplyDefaults()
	finishedIdentity, err := engine.BuildRunIdentity(repoRoot, "owner/repo", headSHA, configPath, finishedPlan)
	if err != nil {
		t.Fatalf("BuildRunIdentity() error = %v", err)
	}
	finishedRun, err := engine.PrepareRun(store, engine.PrepareOptions{
		Identity: finishedIdentity,
		Plan:     finishedPlan,
		Now:      time.Date(2026, 6, 27, 15, 4, 5, 0, time.UTC),
		Random:   bytes.NewReader([]byte{0xde, 0xad, 0xbe, 0xef}),
	})
	if err != nil {
		t.Fatalf("PrepareRun(finished) error = %v", err)
	}
	finishedRun, err = engine.ExecuteRun(t.Context(), store, finishedRun, engine.ExecuteOptions{
		Stdout:   io.Discard,
		Stderr:   io.Discard,
		Progress: io.Discard,
	})
	if err != nil {
		t.Fatalf("ExecuteRun(finished) error = %v", err)
	}

	activePlan := config.ResolvedPlan{Steps: []config.Step{
		{ID: "checks-fast", Command: []string{"/bin/sh", "-c", "printf 'fast\\n'"}},
		{ID: "checks-deep", Command: []string{"/bin/sh", "-c", "printf 'deep\\n'"}, Needs: []string{"checks-fast"}},
	}}
	activePlan.ApplyDefaults()
	activeIdentity, err := engine.BuildRunIdentity(repoRoot, "owner/repo", headSHA, configPath, activePlan)
	if err != nil {
		t.Fatalf("BuildRunIdentity() error = %v", err)
	}
	activeRun, err := engine.PrepareRun(store, engine.PrepareOptions{
		Identity: activeIdentity,
		Plan:     activePlan,
		Now:      time.Date(2026, 6, 27, 15, 5, 5, 0, time.UTC),
		Random:   bytes.NewReader([]byte{0xca, 0xfe, 0xba, 0xbe}),
	})
	if err != nil {
		t.Fatalf("PrepareRun(active) error = %v", err)
	}
	markRunActive(t, store, activeRun, time.Date(2026, 6, 27, 15, 5, 10, 0, time.UTC))

	return cliFixture{root: repoRoot, store: store, finishedRun: finishedRun, activeRun: activeRun}
}

func markRunActive(t *testing.T, store persistence.Store, run engine.RunRecord, startedAt time.Time) {
	t.Helper()

	run.Meta.StartedAt = &startedAt
	run.Meta.FinishedAt = nil
	runnerPID := os.Getpid()
	run.Meta.RunnerPID = &runnerPID
	run.Meta.HeadTreeHash = "head-tree"
	run.Meta.WorktreeTreeHash = "worktree-tree"
	run.Meta.DirtyWorktree = true
	run.Meta.DirtyFiles = []persistence.WorktreeFile{{
		Path:     "README.md",
		Status:   persistence.WorktreeFileModified,
		BlobHash: "blob-123",
	}}
	run.Meta.GitHubPostingSuppressed = "dirty_worktree"
	if err := persistence.WriteJSONFile(store.RunFile(run.RunID, persistence.MetaFile), run.Meta); err != nil {
		t.Fatalf("WriteJSONFile(meta) error = %v", err)
	}

	runningStatus := run.StepStatuses[0]
	runningStatus.State = string(engine.StepStateRunning)
	runningStatus.StartedAt = &startedAt
	runningStatus.FinishedAt = nil
	runningStatus.DurationMillis = 0
	runningStatus.ExitCode = nil
	if err := persistence.WriteJSONFile(store.StepFile(run.RunID, 0, runningStatus.StepID, persistence.StatusFile), runningStatus); err != nil {
		t.Fatalf("WriteJSONFile(running status) error = %v", err)
	}
	if err := persistence.WriteTextFile(store.StepFile(run.RunID, 0, runningStatus.StepID, persistence.StdoutLog), "stdout output\n"); err != nil {
		t.Fatalf("WriteTextFile(stdout) error = %v", err)
	}
	if err := persistence.WriteTextFile(store.StepFile(run.RunID, 0, runningStatus.StepID, persistence.StderrLog), "stderr output\n"); err != nil {
		t.Fatalf("WriteTextFile(stderr) error = %v", err)
	}
	if err := persistence.WriteTextFile(store.StepFile(run.RunID, 0, runningStatus.StepID, persistence.CombinedLog), "combined output\n"); err != nil {
		t.Fatalf("WriteTextFile(combined) error = %v", err)
	}

	summary := persistence.Summary{
		RunID:     run.RunID,
		Status:    string(engine.StepStatePending),
		StartedAt: &startedAt,
		Steps: []persistence.StepSummary{
			{StepID: run.StepStatuses[0].StepID, State: string(engine.StepStateRunning), GitHubContext: run.StepStatuses[0].GitHubContext},
			{StepID: run.StepStatuses[1].StepID, State: string(engine.StepStatePending), GitHubContext: run.StepStatuses[1].GitHubContext},
		},
		Counts: map[string]int{
			string(engine.StepStateRunning): 1,
			string(engine.StepStatePending): 1,
		},
	}
	if err := persistence.WriteJSONFile(store.RunFile(run.RunID, persistence.SummaryFile), summary); err != nil {
		t.Fatalf("WriteJSONFile(summary) error = %v", err)
	}
	if err := persistence.WriteTextFile(store.RunFile(run.RunID, persistence.SummaryText), "active summary\n"); err != nil {
		t.Fatalf("WriteTextFile(summary.txt) error = %v", err)
	}

	appender, err := events.NewAppender(store.RunFile(run.RunID, persistence.EventsFile), run.RunID)
	if err != nil {
		t.Fatalf("NewAppender() error = %v", err)
	}
	if err := appender.Append(startedAt, events.RunStarted, "", string(engine.StepStatePending), ""); err != nil {
		t.Fatalf("Append(run.started) error = %v", err)
	}
	if err := appender.Append(startedAt.Add(time.Second), events.StepStarted, runningStatus.StepID, string(engine.StepStateRunning), ""); err != nil {
		t.Fatalf("Append(step.started) error = %v", err)
	}
}

// advanceStatusPastSummary finishes the active run's first step in status.json only.
func advanceStatusPastSummary(t *testing.T, fixture cliFixture) {
	t.Helper()

	startedAt := time.Date(2026, 6, 27, 15, 6, 0, 0, time.UTC)
	finishedAt := startedAt.Add(time.Second)
	exitCode := 0
	status := fixture.activeRun.StepStatuses[0]
	status.State = string(engine.StepStateSuccess)
	status.StartedAt = &startedAt
	status.FinishedAt = &finishedAt
	status.DurationMillis = finishedAt.Sub(startedAt).Milliseconds()
	status.ExitCode = &exitCode
	if err := persistence.WriteJSONFile(fixture.store.StepFile(fixture.activeRun.RunID, 0, status.StepID, persistence.StatusFile), status); err != nil {
		t.Fatalf("WriteJSONFile(status) error = %v", err)
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()

	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s: %v", args, string(output), err)
	}
}

func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()

	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s: %v", args, string(output), err)
	}
	return strings.TrimSpace(string(output))
}

func timePtr(value time.Time) *time.Time {
	return &value
}

func writeFile(t *testing.T, path string, content []byte) {
	t.Helper()

	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", path, err)
	}
}

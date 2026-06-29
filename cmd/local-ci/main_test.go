package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DiversioTeam/local-ci-runner/internal/config"
	"github.com/DiversioTeam/local-ci-runner/internal/engine"
	"github.com/DiversioTeam/local-ci-runner/internal/events"
	"github.com/DiversioTeam/local-ci-runner/internal/gitrepo"
	"github.com/DiversioTeam/local-ci-runner/internal/persistence"
)

type cliFixture struct {
	root        string
	store       persistence.Store
	finishedRun engine.RunRecord
	activeRun   engine.RunRecord
}

func TestHelpSurfaces(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		args []string
		want string
	}{
		{name: "top level", args: nil, want: "local-ci runs repo-owned verification steps"},
		{name: "top level flag", args: []string{"--help"}, want: "Main commands:"},
		{name: "run", args: []string{"run", "--help"}, want: "Usage:\n  local-ci run"},
		{name: "resume", args: []string{"resume", "--help"}, want: "Usage:\n  local-ci resume"},
		{name: "runs", args: []string{"runs", "--help"}, want: "Usage:\n  local-ci runs"},
		{name: "show", args: []string{"show", "--help"}, want: "Works for both active and finished runs."},
		{name: "publish", args: []string{"publish", "--help"}, want: "Usage:\n  local-ci publish <run-id>"},
		{name: "logs", args: []string{"logs", "--help"}, want: "Defaults:"},
		{name: "help alias", args: []string{"help", "logs"}, want: "--step <id>"},
		{name: "manual", args: []string{"manual"}, want: "## 11. Safety rules and failure modes"},
		{name: "manual help", args: []string{"manual", "--help"}, want: "## 9. Active-run semantics"},
		{name: "help all", args: []string{"help", "all"}, want: "## 9. Active-run semantics"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			stdout := &bytes.Buffer{}
			stderr := &bytes.Buffer{}
			if err := newCLI(stdout, stderr, t.TempDir()).run(testCase.args); err != nil {
				t.Fatalf("run() error = %v", err)
			}
			if got := stdout.String(); !strings.Contains(got, testCase.want) {
				t.Fatalf("stdout = %q, want substring %q", got, testCase.want)
			}
		})
	}
}

func TestManualRejectsUnexpectedArgs(t *testing.T) {
	t.Parallel()

	err := newCLI(&bytes.Buffer{}, &bytes.Buffer{}, t.TempDir()).run([]string{"manual", "foo", "--help"})
	if err == nil {
		t.Fatal("expected error")
	}
	if got, want := err.Error(), "manual accepts no arguments"; !strings.Contains(got, want) {
		t.Fatalf("error = %v, want substring %q", err, want)
	}
}

func TestParseExecutionArgsSupportsNoGitHub(t *testing.T) {
	t.Parallel()

	opts, err := parseExecutionArgs([]string{"--config", "alt.toml", "--no-github"}, false)
	if err != nil {
		t.Fatalf("parseExecutionArgs() error = %v", err)
	}
	if got, want := opts.configPath, "alt.toml"; got != want {
		t.Fatalf("configPath = %q, want %q", got, want)
	}
	if !opts.noGitHub {
		t.Fatal("expected noGitHub=true")
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

func TestValidatePublishableRunRejectsPlanHashChange(t *testing.T) {
	t.Parallel()

	repo := gitrepo.Info{
		Root:             "/repo",
		RepoSlug:         "owner/repo",
		HeadSHA:          "def456",
		HeadTreeHash:     "tree-1",
		WorktreeTreeHash: "tree-1",
		DirtyWorktree:    false,
	}
	identity := engine.RunIdentity{
		RepoRoot:         "/repo",
		RepoSlug:         "owner/repo",
		HeadSHA:          "def456",
		ConfigPath:       "/repo/.local-ci.toml",
		ConfigHash:       "cfg-1",
		PlanHash:         "plan-2",
		WorktreeTreeHash: "tree-1",
	}
	run := engine.RunRecord{
		RunID: "run-1",
		Meta: persistence.Meta{
			RepoRoot:                "/repo",
			RepoSlug:                "owner/repo",
			ConfigPath:              "/repo/.local-ci.toml",
			ConfigHash:              "cfg-1",
			PlanHash:                "plan-1",
			GitHubEnabled:           true,
			GitHubPostingSuppressed: "dirty_worktree",
			WorktreeTreeHash:        "tree-1",
			StartedAt:               timePtr(time.Date(2026, 6, 27, 15, 0, 0, 0, time.UTC)),
			FinishedAt:              timePtr(time.Date(2026, 6, 27, 15, 1, 0, 0, time.UTC)),
		},
		Summary: persistence.Summary{Status: string(engine.StepStateSuccess)},
	}

	err := validatePublishableRun(repo, identity, run)
	if err == nil {
		t.Fatal("expected error")
	}
	if got, want := err.Error(), "plan hash changed"; !strings.Contains(got, want) {
		t.Fatalf("error = %v, want substring %q", err, want)
	}
}

func TestValidatePublishableRunRejectsAlreadyPostedRun(t *testing.T) {
	t.Parallel()

	repo := gitrepo.Info{
		Root:             "/repo",
		RepoSlug:         "owner/repo",
		HeadSHA:          "def456",
		HeadTreeHash:     "tree-1",
		WorktreeTreeHash: "tree-1",
		DirtyWorktree:    false,
	}
	identity := engine.RunIdentity{
		RepoRoot:         "/repo",
		RepoSlug:         "owner/repo",
		HeadSHA:          "def456",
		ConfigPath:       "/repo/.local-ci.toml",
		ConfigHash:       "cfg-1",
		PlanHash:         "plan-1",
		WorktreeTreeHash: "tree-1",
	}
	run := engine.RunRecord{
		RunID: "run-1",
		Meta: persistence.Meta{
			RepoRoot:         "/repo",
			RepoSlug:         "owner/repo",
			ConfigPath:       "/repo/.local-ci.toml",
			ConfigHash:       "cfg-1",
			PlanHash:         "plan-1",
			GitHubEnabled:    true,
			WorktreeTreeHash: "tree-1",
			StartedAt:        timePtr(time.Date(2026, 6, 27, 15, 0, 0, 0, time.UTC)),
			FinishedAt:       timePtr(time.Date(2026, 6, 27, 15, 1, 0, 0, time.UTC)),
		},
		Summary: persistence.Summary{Status: string(engine.StepStateSuccess)},
	}

	err := validatePublishableRun(repo, identity, run)
	if err == nil {
		t.Fatal("expected error")
	}
	if got, want := err.Error(), "already posted during execution"; !strings.Contains(got, want) {
		t.Fatalf("error = %v, want substring %q", err, want)
	}
}

func TestRunsListsNewestFirstAndMarksActiveRun(t *testing.T) {
	t.Parallel()

	fixture := newCLIFixture(t)
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	if err := newCLI(stdout, stderr, fixture.root).run([]string{"runs"}); err != nil {
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
	if !strings.Contains(output, "4242") {
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
	if err := newCLI(activeOut, &bytes.Buffer{}, fixture.root).run([]string{"show", fixture.activeRun.RunID}); err != nil {
		t.Fatalf("show active error = %v", err)
	}
	activeText := activeOut.String()
	if !strings.Contains(activeText, "status: running") {
		t.Fatalf("active show missing running status:\n%s", activeText)
	}
	if !strings.Contains(activeText, "pid: 4242") {
		t.Fatalf("active show missing runner pid:\n%s", activeText)
	}
	if !strings.Contains(activeText, "head_tree: head-tree") || !strings.Contains(activeText, "worktree_tree: worktree-tree") {
		t.Fatalf("active show missing snapshot hashes:\n%s", activeText)
	}
	if !strings.Contains(activeText, "github_posting: suppressed (dirty_worktree)") {
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
	if err := newCLI(finishedOut, &bytes.Buffer{}, fixture.root).run([]string{"show", fixture.finishedRun.RunID}); err != nil {
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

func TestLogsDefaultsToRunnerAndSupportsStepViews(t *testing.T) {
	t.Parallel()

	fixture := newCLIFixture(t)

	runnerOut := &bytes.Buffer{}
	if err := newCLI(runnerOut, &bytes.Buffer{}, fixture.root).run([]string{"logs", fixture.activeRun.RunID}); err != nil {
		t.Fatalf("runner logs error = %v", err)
	}
	if got := runnerOut.String(); !strings.Contains(got, "run started") || !strings.Contains(got, "start checks-fast") {
		t.Fatalf("runner logs output = %q", got)
	}

	combinedOut := &bytes.Buffer{}
	if err := newCLI(combinedOut, &bytes.Buffer{}, fixture.root).run([]string{"logs", fixture.activeRun.RunID, "--step", "checks-fast"}); err != nil {
		t.Fatalf("combined step logs error = %v", err)
	}
	if got := combinedOut.String(); !strings.Contains(got, "combined output") {
		t.Fatalf("combined logs output = %q", got)
	}

	stderrOut := &bytes.Buffer{}
	if err := newCLI(stderrOut, &bytes.Buffer{}, fixture.root).run([]string{"logs", fixture.activeRun.RunID, "--step", "checks-fast", "--stderr"}); err != nil {
		t.Fatalf("stderr step logs error = %v", err)
	}
	if got := stderrOut.String(); !strings.Contains(got, "stderr output") {
		t.Fatalf("stderr logs output = %q", got)
	}
}

func TestLogsRejectsConflictingSelectors(t *testing.T) {
	t.Parallel()

	fixture := newCLIFixture(t)
	err := newCLI(&bytes.Buffer{}, &bytes.Buffer{}, fixture.root).run([]string{"logs", fixture.activeRun.RunID, "--runner", "--planner"})
	if err == nil {
		t.Fatal("expected error")
	}
	if got, want := err.Error(), "choose exactly one log source"; !strings.Contains(got, want) {
		t.Fatalf("error = %v, want substring %q", err, want)
	}
}

func TestLogsRejectsMissingStepValue(t *testing.T) {
	t.Parallel()

	fixture := newCLIFixture(t)
	err := newCLI(&bytes.Buffer{}, &bytes.Buffer{}, fixture.root).run([]string{"logs", fixture.activeRun.RunID, "--step", "--stderr"})
	if err == nil {
		t.Fatal("expected error")
	}
	if got, want := err.Error(), "--step requires a value"; !strings.Contains(got, want) {
		t.Fatalf("error = %v, want substring %q", err, want)
	}
}

func TestShowFallsBackToBestEffortSnapshotDuringSummaryDrift(t *testing.T) {
	t.Parallel()

	fixture := newCLIFixture(t)
	driftActiveRun(t, fixture)

	stdout := &bytes.Buffer{}
	if err := newCLI(stdout, &bytes.Buffer{}, fixture.root).run([]string{"show", fixture.activeRun.RunID}); err != nil {
		t.Fatalf("show error = %v", err)
	}
	if got := stdout.String(); !strings.Contains(got, "status: running") {
		t.Fatalf("show output = %q, want running status", got)
	}
}

func TestRunsFallsBackToBestEffortSnapshotDuringSummaryDrift(t *testing.T) {
	t.Parallel()

	fixture := newCLIFixture(t)
	driftActiveRun(t, fixture)

	stdout := &bytes.Buffer{}
	if err := newCLI(stdout, &bytes.Buffer{}, fixture.root).run([]string{"runs"}); err != nil {
		t.Fatalf("runs error = %v", err)
	}
	if got := stdout.String(); !strings.Contains(got, fixture.activeRun.RunID) || !strings.Contains(got, "running") {
		t.Fatalf("runs output = %q, want active running run", got)
	}
}

func TestJSONModes(t *testing.T) {
	t.Parallel()

	fixture := newCLIFixture(t)

	runsOut := &bytes.Buffer{}
	if err := newCLI(runsOut, &bytes.Buffer{}, fixture.root).run([]string{"runs", "--json"}); err != nil {
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
	if err := newCLI(showOut, &bytes.Buffer{}, fixture.root).run([]string{"show", fixture.activeRun.RunID, "--json"}); err != nil {
		t.Fatalf("show --json error = %v", err)
	}
	var showPayload showJSON
	if err := json.Unmarshal(showOut.Bytes(), &showPayload); err != nil {
		t.Fatalf("decode show json: %v", err)
	}
	if got, want := showPayload.Status, string(engine.StepStateRunning); got != want {
		t.Fatalf("show status = %q, want %q", got, want)
	}

	logsOut := &bytes.Buffer{}
	if err := newCLI(logsOut, &bytes.Buffer{}, fixture.root).run([]string{"logs", fixture.activeRun.RunID, "--json"}); err != nil {
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

func newCLIFixture(t *testing.T) cliFixture {
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
	finishedRun, err = engine.ExecuteRun(store, finishedRun, engine.ExecuteOptions{
		Context:  context.Background(),
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
	runnerPID := 4242
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

func driftActiveRun(t *testing.T, fixture cliFixture) {
	t.Helper()

	startedAt := time.Date(2026, 6, 27, 15, 6, 0, 0, time.UTC)
	activeStatus := fixture.activeRun.StepStatuses[0]
	activeStatus.State = string(engine.StepStateRunning)
	activeStatus.StartedAt = &startedAt
	activeStatus.FinishedAt = nil
	activeStatus.DurationMillis = 0
	activeStatus.ExitCode = nil
	if err := persistence.WriteJSONFile(fixture.store.StepFile(fixture.activeRun.RunID, 0, activeStatus.StepID, persistence.StatusFile), activeStatus); err != nil {
		t.Fatalf("WriteJSONFile(active status) error = %v", err)
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

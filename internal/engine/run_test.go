package engine

import (
	"bytes"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DiversioTeam/local-ci-runner/internal/config"
	"github.com/DiversioTeam/local-ci-runner/internal/persistence"
)

var (
	fixedRunTime    = time.Date(2026, 6, 27, 15, 4, 5, 0, time.UTC)
	fixedRunEntropy = []byte{0xde, 0xad, 0xbe, 0xef}
)

type runFixture struct {
	repoRoot   string
	configPath string
	plan       config.ResolvedPlan
	github     config.GitHub
	identity   RunIdentity
	store      persistence.Store
}

func TestPrepareRunWritesArtifacts(t *testing.T) {
	t.Parallel()

	fixture := newRunFixture(t, samplePlan())
	run, err := PrepareRun(fixture.store, PrepareOptions{
		Identity:   fixture.identity,
		Plan:       fixture.plan,
		PlannerLog: "planner log\n",
		Now:        fixedRunTime,
		Random:     bytes.NewReader(cloneBytes(fixedRunEntropy)),
	})
	if err != nil {
		t.Fatalf("PrepareRun() error = %v", err)
	}

	for _, path := range []string{
		fixture.store.RunFile(run.RunID, persistence.MetaFile),
		fixture.store.RunFile(run.RunID, persistence.PlanFile),
		fixture.store.RunFile(run.RunID, persistence.PlanEnvFile),
		fixture.store.RunFile(run.RunID, persistence.SummaryFile),
		fixture.store.RunFile(run.RunID, persistence.SummaryText),
		fixture.store.RunFile(run.RunID, persistence.EventsFile),
		fixture.store.RunFile(run.RunID, persistence.PlannerLogFile),
		fixture.store.StepFile(run.RunID, 0, "lint", persistence.StatusFile),
		fixture.store.StepFile(run.RunID, 0, "lint", persistence.StdoutLog),
		fixture.store.StepFile(run.RunID, 0, "lint", persistence.StderrLog),
		fixture.store.StepFile(run.RunID, 0, "lint", persistence.CombinedLog),
		fixture.store.StepFile(run.RunID, 0, "lint", persistence.OutputEnv),
		fixture.store.StepFile(run.RunID, 1, "test", persistence.StatusFile),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected artifact %s: %v", path, err)
		}
	}

	loaded, err := LoadRunForResume(fixture.store, run.RunID, fixture.identity)
	if err != nil {
		t.Fatalf("LoadRunForResume() error = %v", err)
	}
	if got, want := loaded.Meta.PlanHash, fixture.identity.PlanHash; got != want {
		t.Fatalf("PlanHash = %q, want %q", got, want)
	}
	if got, want := loaded.Summary.Status, runStatusPending; got != want {
		t.Fatalf("Summary.Status = %q, want %q", got, want)
	}
	if len(loaded.StepStatuses) != 2 {
		t.Fatalf("step status count = %d, want 2", len(loaded.StepStatuses))
	}
	if got, want := loaded.StepStatuses[0].State, string(StepStatePending); got != want {
		t.Fatalf("step state = %q, want %q", got, want)
	}

	planEnv, err := persistence.ReadEnvFile(fixture.store.RunFile(run.RunID, persistence.PlanEnvFile))
	if err != nil {
		t.Fatalf("ReadEnvFile() error = %v", err)
	}
	if got, want := planEnv["CHANGED_SCOPE"], "python"; got != want {
		t.Fatalf("plan env = %q, want %q", got, want)
	}
	if got, want := mustReadFile(t, fixture.store.RunFile(run.RunID, persistence.PlannerLogFile)), "planner log\n"; got != want {
		t.Fatalf("planner log = %q, want %q", got, want)
	}
}

func TestPrepareRunRejectsIdentityDrift(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name      string
		change    func(t *testing.T, fixture *runFixture, options *PrepareOptions)
		wantError string
	}{
		{name: "store at another root", change: func(t *testing.T, _ *runFixture, options *PrepareOptions) { options.Identity.RepoRoot = t.TempDir() }, wantError: "store root"},
		{
			name: "config rewritten after hashing",
			change: func(t *testing.T, fixture *runFixture, _ *PrepareOptions) {
				writeFile(t, fixture.configPath, []byte("version = 1\n# changed\n"))
			},
			wantError: "config hash does not match current config file",
		},
		{name: "plan changed after hashing", change: func(_ *testing.T, _ *runFixture, options *PrepareOptions) { options.Identity.PlanHash = "wrong" }, wantError: "plan hash does not match current plan"},
		{name: "posting without an aggregate context", change: func(_ *testing.T, _ *runFixture, options *PrepareOptions) {
			options.GitHub = config.GitHub{Enabled: true}
		}, wantError: "aggregate context is required"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fixture := newRunFixture(t, samplePlan())
			options := PrepareOptions{Identity: fixture.identity, Plan: fixture.plan, Now: fixedRunTime, Random: bytes.NewReader(cloneBytes(fixedRunEntropy))}
			test.change(t, &fixture, &options)
			if _, err := PrepareRun(fixture.store, options); err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("error = %v, want %q", err, test.wantError)
			}
		})
	}
}

func TestLoadRunForResumeRejectsIdentityChanges(t *testing.T) {
	t.Parallel()

	fixture := newRunFixture(t, samplePlan())
	run := prepareRunFixture(t, fixture)

	writeFile(t, fixture.configPath, []byte("version = 1\n# changed\n"))
	changedIdentity, err := BuildRunIdentity(fixture.repoRoot, "owner/repo", "abc123", fixture.configPath, fixture.plan)
	if err != nil {
		t.Fatalf("BuildRunIdentity() error = %v", err)
	}

	_, err = LoadRunForResume(fixture.store, run.RunID, changedIdentity)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "config hash changed") {
		t.Fatalf("error = %v", err)
	}
}

func TestValidateResumeRejectsIdentityChanges(t *testing.T) {
	t.Parallel()

	identity := RunIdentity{
		RepoRoot:         "/repo",
		RepoSlug:         "owner/repo",
		HeadSHA:          "abc123",
		ConfigPath:       "/repo/.local-ci.toml",
		ConfigHash:       "cfg",
		PlanHash:         "plan",
		WorktreeTreeHash: "tree-1",
	}
	meta := persistence.Meta{
		RepoRoot:         identity.RepoRoot,
		RepoSlug:         identity.RepoSlug,
		HeadSHA:          identity.HeadSHA,
		ConfigPath:       identity.ConfigPath,
		ConfigHash:       identity.ConfigHash,
		PlanHash:         identity.PlanHash,
		WorktreeTreeHash: identity.WorktreeTreeHash,
	}

	cases := []struct {
		name    string
		mutate  func(RunIdentity) RunIdentity
		wantErr string
	}{
		{
			name: "repo root",
			mutate: func(current RunIdentity) RunIdentity {
				current.RepoRoot = "/other"
				return current
			},
			wantErr: "repo root changed",
		},
		{
			name: "repo slug",
			mutate: func(current RunIdentity) RunIdentity {
				current.RepoSlug = "other/repo"
				return current
			},
			wantErr: "repo slug changed",
		},
		{
			name: "head sha",
			mutate: func(current RunIdentity) RunIdentity {
				current.HeadSHA = "def456"
				return current
			},
			wantErr: "HEAD SHA changed",
		},
		{
			// resume --config can point at an identical file somewhere else.
			name: "config path",
			mutate: func(current RunIdentity) RunIdentity {
				current.ConfigPath = "/repo/other.toml"
				return current
			},
			wantErr: "config path changed",
		},
		{
			name: "config hash",
			mutate: func(current RunIdentity) RunIdentity {
				current.ConfigHash = "other"
				return current
			},
			wantErr: "config hash changed",
		},
		{
			name: "plan hash",
			mutate: func(current RunIdentity) RunIdentity {
				current.PlanHash = "other"
				return current
			},
			wantErr: "plan hash changed",
		},
		{
			name: "worktree tree hash",
			mutate: func(current RunIdentity) RunIdentity {
				current.WorktreeTreeHash = "tree-2"
				return current
			},
			wantErr: "worktree tree hash changed",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			current := testCase.mutate(identity)
			err := ValidateResume(meta, current)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), testCase.wantErr) {
				t.Fatalf("error = %v, want %q", err, testCase.wantErr)
			}
		})
	}
}

func TestMarkStaleFromStepMarksDownstream(t *testing.T) {
	t.Parallel()

	// Rerunning "lint" must reach "package" through "test", not just direct dependents.
	plan := config.ResolvedPlan{Steps: []config.Step{
		{ID: "root", Command: []string{"./scripts/root.sh"}},
		{ID: "lint", Command: []string{"./scripts/lint.sh"}, Needs: []string{"root"}},
		{ID: "test", Command: []string{"./scripts/test.sh"}, Needs: []string{"lint"}},
		{ID: "package", Command: []string{"./scripts/package.sh"}, Needs: []string{"test"}},
		{ID: "docs", Command: []string{"./scripts/docs.sh"}},
	}}
	plan.ApplyDefaults()

	statuses := InitialStepStatuses(plan)
	for index := range statuses {
		exitCode := 0
		setCompletedState(&statuses[index], StepStateSuccess, fixedRunTime, fixedRunTime, &exitCode)
	}

	updated, err := MarkStaleFromStep(plan, statuses, "lint")
	if err != nil {
		t.Fatalf("MarkStaleFromStep() error = %v", err)
	}
	want := map[string]string{"root": "success", "lint": "stale", "test": "stale", "package": "stale", "docs": "success"}
	if got := statusStates(updated); !maps.Equal(got, want) {
		t.Fatalf("states = %v, want %v", got, want)
	}
}

func TestLoadRunRejectsRunIDDrift(t *testing.T) {
	t.Parallel()

	fixture := newRunFixture(t, samplePlan())
	run := prepareRunFixture(t, fixture)

	meta := run.Meta
	meta.RunID = "other-run"
	if err := persistence.WriteJSONFile(fixture.store.RunFile(run.RunID, persistence.MetaFile), meta); err != nil {
		t.Fatalf("WriteJSONFile() error = %v", err)
	}

	_, err := LoadRun(fixture.store, run.RunID)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "stored run id does not match requested run directory") {
		t.Fatalf("error = %v", err)
	}
}

func TestLoadRunRejectsSummaryDrift(t *testing.T) {
	t.Parallel()

	fixture := newRunFixture(t, samplePlan())
	run := prepareRunFixture(t, fixture)

	summary := run.Summary
	summary.Status = string(StepStateSuccess)
	if err := persistence.WriteJSONFile(fixture.store.RunFile(run.RunID, persistence.SummaryFile), summary); err != nil {
		t.Fatalf("WriteJSONFile() error = %v", err)
	}

	_, err := LoadRun(fixture.store, run.RunID)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrStoredSummaryMismatch) {
		t.Fatalf("LoadRun() error = %v, want ErrStoredSummaryMismatch", err)
	}
}

func TestSummarizeRunStatusPrefersPendingWhileWorkIsActive(t *testing.T) {
	t.Parallel()

	counts := map[string]int{
		string(StepStateRunning): 1,
		string(StepStateBlocked): 1,
	}

	if got, want := summarizeRunStatus(counts), runStatusPending; got != want {
		t.Fatalf("summarizeRunStatus() = %q, want %q", got, want)
	}
}

func TestPrepareRunWithEmptyPlanStartsSuccessful(t *testing.T) {
	t.Parallel()

	fixture := newRunFixture(t, config.ResolvedPlan{})
	run := prepareRunFixture(t, fixture)

	if got, want := run.Summary.Status, string(StepStateSuccess); got != want {
		t.Fatalf("Summary.Status = %q, want %q", got, want)
	}
}

func TestLoadRunRejectsStepStatusStateDrift(t *testing.T) {
	t.Parallel()

	fixture := newRunFixture(t, samplePlan())
	run := prepareRunFixture(t, fixture)

	status, err := persistence.ReadJSONFile[persistence.StepStatus](fixture.store.StepFile(run.RunID, 0, "lint", persistence.StatusFile))
	if err != nil {
		t.Fatalf("ReadJSONFile() error = %v", err)
	}
	status.State = string(StepStateSuccess)
	if err := persistence.WriteJSONFile(fixture.store.StepFile(run.RunID, 0, "lint", persistence.StatusFile), status); err != nil {
		t.Fatalf("WriteJSONFile() error = %v", err)
	}

	_, err = LoadRun(fixture.store, run.RunID)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "stored successful step state requires start and finish times") {
		t.Fatalf("error = %v", err)
	}
}

func TestLoadRunRejectsStepStatusDrift(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name      string
		change    func(*persistence.StepStatus, *persistence.Summary)
		wantError string
	}{
		{name: "log path", change: func(status *persistence.StepStatus, _ *persistence.Summary) { status.StdoutLog = "wrong.log" }, wantError: "stored stdout log path does not match persisted plan"},
		{
			// The summary is rewritten too, so only the status-against-plan check can refuse it.
			name: "step id",
			change: func(status *persistence.StepStatus, summary *persistence.Summary) {
				status.StepID, summary.Steps[0].StepID = "other", "other"
			},
			wantError: "stored step id does not match persisted plan",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fixture := newRunFixture(t, samplePlan())
			run := prepareRunFixture(t, fixture)
			statusPath := fixture.store.StepFile(run.RunID, 0, "lint", persistence.StatusFile)
			summaryPath := fixture.store.RunFile(run.RunID, persistence.SummaryFile)
			status, err := persistence.ReadJSONFile[persistence.StepStatus](statusPath)
			if err != nil {
				t.Fatal(err)
			}
			summary, err := persistence.ReadJSONFile[persistence.Summary](summaryPath)
			if err != nil {
				t.Fatal(err)
			}
			test.change(&status, &summary)
			if err := persistence.WriteJSONFile(statusPath, status); err != nil {
				t.Fatal(err)
			}
			if err := persistence.WriteJSONFile(summaryPath, summary); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadRun(fixture.store, run.RunID); err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("error = %v, want %q", err, test.wantError)
			}
		})
	}
}

func samplePlan() config.ResolvedPlan {
	plan := config.ResolvedPlan{
		Env: map[string]string{"CHANGED_SCOPE": "python"},
		Steps: []config.Step{
			{ID: "lint", Command: []string{"./scripts/lint.sh"}},
			{ID: "test", Command: []string{"./scripts/test.sh"}, Needs: []string{"lint"}},
		},
	}
	plan.ApplyDefaults()
	return plan
}

func newRunFixture(t *testing.T, plan config.ResolvedPlan) runFixture {
	return newRunFixtureWithGitHub(t, plan, config.GitHub{})
}

func newRunFixtureWithGitHub(t *testing.T, plan config.ResolvedPlan, githubConfig config.GitHub) runFixture {
	t.Helper()

	if githubConfig.Enabled && githubConfig.AggregateContext == "" {
		githubConfig.AggregateContext = config.DefaultAggregateContext
	}
	repoRoot := t.TempDir()
	configPath := filepath.Join(repoRoot, config.DefaultPath)
	writeFile(t, configPath, []byte("version = 1\n"))

	identity, err := BuildRunIdentity(repoRoot, "owner/repo", "abc123", config.DefaultPath, plan)
	if err != nil {
		t.Fatalf("BuildRunIdentity() error = %v", err)
	}

	return runFixture{
		repoRoot:   repoRoot,
		configPath: configPath,
		plan:       plan,
		github:     githubConfig,
		identity:   identity,
		store:      persistence.NewStore(repoRoot),
	}
}

func prepareRunFixture(t *testing.T, fixture runFixture) RunRecord {
	t.Helper()
	return prepareSuppressedRunFixture(t, fixture, "")
}

// prepareSuppressedRunFixture records why posting is suppressed, as `run --no-github` does.
// Only such runs are publishable, so publication tests start here rather than editing a result.
func prepareSuppressedRunFixture(t *testing.T, fixture runFixture, suppressedReason string) RunRecord {
	t.Helper()

	run, err := PrepareRun(fixture.store, PrepareOptions{
		Identity:                fixture.identity,
		Plan:                    fixture.plan,
		GitHub:                  fixture.github,
		GitHubPostingSuppressed: suppressedReason,
		Now:                     fixedRunTime,
		Random:                  bytes.NewReader(cloneBytes(fixedRunEntropy)),
	})
	if err != nil {
		t.Fatalf("PrepareRun() error = %v", err)
	}

	return run
}

func cloneBytes(src []byte) []byte {
	return append([]byte(nil), src...)
}

func writeFile(t *testing.T, path string, content []byte) {
	t.Helper()

	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", path, err)
	}
}

package engine

import (
	"bytes"
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

func TestPrepareRunWritesPlannerLog(t *testing.T) {
	t.Parallel()

	fixture := newRunFixture(t, samplePlan())
	run, err := PrepareRun(fixture.store, PrepareOptions{
		Identity:   fixture.identity,
		Plan:       fixture.plan,
		GitHub:     fixture.github,
		PlannerLog: "planner log\n",
		Now:        fixedRunTime,
		Random:     bytes.NewReader(cloneBytes(fixedRunEntropy)),
	})
	if err != nil {
		t.Fatalf("PrepareRun() error = %v", err)
	}

	if got, want := mustReadTextFile(t, fixture.store.RunFile(run.RunID, persistence.PlannerLogFile)), "planner log\n"; got != want {
		t.Fatalf("planner log = %q, want %q", got, want)
	}
}

func TestPrepareRunWritesArtifacts(t *testing.T) {
	t.Parallel()

	fixture := newRunFixture(t, samplePlan())
	run := prepareRunFixture(t, fixture)

	if got, want := run.RunID, "20260627T150405Z-deadbeef"; got != want {
		t.Fatalf("RunID = %q, want %q", got, want)
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
}

func TestPrepareRunAppliesDefaultGitHubAggregateContext(t *testing.T) {
	t.Parallel()

	fixture := newRunFixtureWithGitHub(t, samplePlan(), config.GitHub{Enabled: true})
	run := prepareRunFixture(t, fixture)

	if got, want := run.Meta.GitHubAggregateContext, config.DefaultAggregateContext; got != want {
		t.Fatalf("GitHubAggregateContext = %q, want %q", got, want)
	}
}

func TestPrepareRunRejectsIdentityDrift(t *testing.T) {
	t.Parallel()

	fixture := newRunFixture(t, samplePlan())
	badIdentity := fixture.identity
	badIdentity.PlanHash = "wrong"

	_, err := PrepareRun(fixture.store, PrepareOptions{
		Identity: badIdentity,
		Plan:     fixture.plan,
		Now:      fixedRunTime,
		Random:   bytes.NewReader(cloneBytes(fixedRunEntropy)),
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "plan hash does not match current plan") {
		t.Fatalf("error = %v", err)
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

	plan := config.ResolvedPlan{Steps: []config.Step{
		{ID: "lint", Command: []string{"./scripts/lint.sh"}},
		{ID: "test", Command: []string{"./scripts/test.sh"}, Needs: []string{"lint"}},
		{ID: "package", Command: []string{"./scripts/package.sh"}, Needs: []string{"test"}},
		{ID: "docs", Command: []string{"./scripts/docs.sh"}},
	}}
	plan.ApplyDefaults()

	statuses := InitialStepStatuses(plan)
	completedAt := fixedRunTime
	for index := range statuses {
		startedAt := completedAt
		exitCode := 0
		statuses[index].State = string(StepStateSuccess)
		statuses[index].StartedAt = &startedAt
		statuses[index].FinishedAt = &completedAt
		statuses[index].ExitCode = &exitCode
	}

	updated, err := MarkStaleFromStep(plan, statuses, "test")
	if err != nil {
		t.Fatalf("MarkStaleFromStep() error = %v", err)
	}

	states := map[string]string{}
	for _, status := range updated {
		states[status.StepID] = status.State
	}

	if got, want := states["lint"], string(StepStateSuccess); got != want {
		t.Fatalf("lint state = %q, want %q", got, want)
	}
	if got, want := states["test"], string(StepStateStale); got != want {
		t.Fatalf("test state = %q, want %q", got, want)
	}
	if got, want := states["package"], string(StepStateStale); got != want {
		t.Fatalf("package state = %q, want %q", got, want)
	}
	if got, want := states["docs"], string(StepStateSuccess); got != want {
		t.Fatalf("docs state = %q, want %q", got, want)
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
	if !strings.Contains(err.Error(), "stored summary status does not match step statuses") {
		t.Fatalf("error = %v", err)
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

func TestMarkStaleFromStepRejectsUnknownStatus(t *testing.T) {
	t.Parallel()

	plan := samplePlan()
	statuses := []persistence.StepStatus{{StepID: "other", State: string(StepStateSuccess)}}

	_, err := MarkStaleFromStep(plan, statuses, "lint")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), `unknown step status "other"`) {
		t.Fatalf("error = %v", err)
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

	fixture := newRunFixture(t, samplePlan())
	run := prepareRunFixture(t, fixture)

	status, err := persistence.ReadJSONFile[persistence.StepStatus](fixture.store.StepFile(run.RunID, 0, "lint", persistence.StatusFile))
	if err != nil {
		t.Fatalf("ReadJSONFile() error = %v", err)
	}
	status.StdoutLog = "wrong.log"
	if err := persistence.WriteJSONFile(fixture.store.StepFile(run.RunID, 0, "lint", persistence.StatusFile), status); err != nil {
		t.Fatalf("WriteJSONFile() error = %v", err)
	}

	_, err = LoadRun(fixture.store, run.RunID)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "stored stdout log path does not match persisted plan") {
		t.Fatalf("error = %v", err)
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

	run, err := PrepareRun(fixture.store, PrepareOptions{
		Identity: fixture.identity,
		Plan:     fixture.plan,
		GitHub:   fixture.github,
		Now:      fixedRunTime,
		Random:   bytes.NewReader(cloneBytes(fixedRunEntropy)),
	})
	if err != nil {
		t.Fatalf("PrepareRun() error = %v", err)
	}

	return run
}

func cloneBytes(src []byte) []byte {
	return append([]byte(nil), src...)
}

func mustReadTextFile(t *testing.T, path string) string {
	t.Helper()

	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s) error = %v", path, err)
	}
	return string(payload)
}

func writeFile(t *testing.T, path string, content []byte) {
	t.Helper()

	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", path, err)
	}
}

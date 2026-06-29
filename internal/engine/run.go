package engine

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/DiversioTeam/local-ci-runner/internal/config"
	"github.com/DiversioTeam/local-ci-runner/internal/persistence"
)

const runStatusPending = "pending"

type PrepareOptions struct {
	Identity                RunIdentity
	Plan                    config.ResolvedPlan
	GitHub                  config.GitHub
	PlannerLog              string
	HeadTreeHash            string
	WorktreeTreeHash        string
	DirtyWorktree           bool
	DirtyFiles              []persistence.WorktreeFile
	GitHubPostingSuppressed string
	Now                     time.Time
	Random                  io.Reader
}

type RunRecord struct {
	RunID        string
	RunDir       string
	Meta         persistence.Meta
	Plan         config.ResolvedPlan
	Summary      persistence.Summary
	StepStatuses []persistence.StepStatus
}

func PrepareRun(store persistence.Store, opts PrepareOptions) (RunRecord, error) {
	if err := opts.Identity.Validate(); err != nil {
		return RunRecord{}, err
	}
	githubConfig := normalizeGitHubConfig(opts.GitHub)

	plan := config.ResolvedPlan{
		Env:   cloneStringMap(opts.Plan.Env),
		Steps: cloneSteps(opts.Plan.Steps),
	}
	plan.ApplyDefaults()
	if err := plan.Validate(); err != nil {
		return RunRecord{}, fmt.Errorf("validate plan: %w", err)
	}
	if err := validatePrepareInputs(store, opts.Identity, plan, githubConfig); err != nil {
		return RunRecord{}, err
	}

	now := opts.Now.UTC()
	if opts.Now.IsZero() {
		now = time.Now().UTC()
	}

	runID, err := NewRunID(now, opts.Random)
	if err != nil {
		return RunRecord{}, err
	}
	if err := store.EnsureRun(runID); err != nil {
		return RunRecord{}, err
	}

	meta := persistence.Meta{
		RunID:                   runID,
		RepoRoot:                opts.Identity.RepoRoot,
		RepoSlug:                opts.Identity.RepoSlug,
		HeadSHA:                 opts.Identity.HeadSHA,
		ConfigPath:              opts.Identity.ConfigPath,
		ConfigHash:              opts.Identity.ConfigHash,
		PlanHash:                opts.Identity.PlanHash,
		GitHubEnabled:           githubConfig.Enabled,
		GitHubAggregateContext:  githubConfig.AggregateContext,
		CreatedAt:               now,
		HeadTreeHash:            opts.HeadTreeHash,
		WorktreeTreeHash:        opts.WorktreeTreeHash,
		DirtyWorktree:           opts.DirtyWorktree,
		DirtyFiles:              cloneWorktreeFiles(opts.DirtyFiles),
		GitHubPostingSuppressed: opts.GitHubPostingSuppressed,
	}
	if err := persistence.WriteJSONFile(store.RunFile(runID, persistence.MetaFile), meta); err != nil {
		return RunRecord{}, err
	}
	if err := persistence.WriteJSONFile(store.RunFile(runID, persistence.PlanFile), plan); err != nil {
		return RunRecord{}, err
	}
	if err := persistence.WriteEnvFile(store.RunFile(runID, persistence.PlanEnvFile), plan.Env); err != nil {
		return RunRecord{}, err
	}
	if err := persistence.TouchFile(store.RunFile(runID, persistence.EventsFile)); err != nil {
		return RunRecord{}, err
	}
	if err := persistence.WriteTextFile(store.RunFile(runID, persistence.PlannerLogFile), opts.PlannerLog); err != nil {
		return RunRecord{}, err
	}

	stepStatuses := InitialStepStatuses(plan)
	for stepIndex, status := range stepStatuses {
		if err := store.EnsureStepDir(runID, stepIndex, status.StepID); err != nil {
			return RunRecord{}, err
		}
		for _, name := range []string{persistence.StdoutLog, persistence.StderrLog, persistence.CombinedLog, persistence.OutputEnv} {
			if err := persistence.TouchFile(store.StepFile(runID, stepIndex, status.StepID, name)); err != nil {
				return RunRecord{}, err
			}
		}
		if err := persistence.WriteJSONFile(store.StepFile(runID, stepIndex, status.StepID, persistence.StatusFile), status); err != nil {
			return RunRecord{}, err
		}
	}

	summary := buildSummary(runID, stepStatuses, nil, nil)
	if err := persistence.WriteJSONFile(store.RunFile(runID, persistence.SummaryFile), summary); err != nil {
		return RunRecord{}, err
	}
	if err := persistence.WriteTextFile(store.RunFile(runID, persistence.SummaryText), renderSummaryText(store.RunDir(runID), meta, summary, stepStatuses)); err != nil {
		return RunRecord{}, err
	}

	return RunRecord{
		RunID:        runID,
		RunDir:       store.RunDir(runID),
		Meta:         meta,
		Plan:         plan,
		Summary:      summary,
		StepStatuses: stepStatuses,
	}, nil
}

func LoadRun(store persistence.Store, runID string) (RunRecord, error) {
	meta, err := persistence.ReadJSONFile[persistence.Meta](store.RunFile(runID, persistence.MetaFile))
	if err != nil {
		return RunRecord{}, err
	}
	if meta.RunID != runID {
		return RunRecord{}, fmt.Errorf("stored run id does not match requested run directory")
	}
	plan, err := persistence.ReadJSONFile[config.ResolvedPlan](store.RunFile(runID, persistence.PlanFile))
	if err != nil {
		return RunRecord{}, err
	}
	plan.ApplyDefaults()
	if err := plan.Validate(); err != nil {
		return RunRecord{}, fmt.Errorf("validate stored plan: %w", err)
	}
	storedPlanHash, err := HashPlan(plan)
	if err != nil {
		return RunRecord{}, err
	}
	if meta.PlanHash != storedPlanHash {
		return RunRecord{}, fmt.Errorf("stored plan hash does not match persisted plan")
	}

	summary, err := persistence.ReadJSONFile[persistence.Summary](store.RunFile(runID, persistence.SummaryFile))
	if err != nil {
		return RunRecord{}, err
	}

	stepStatuses := make([]persistence.StepStatus, 0, len(plan.Steps))
	for stepIndex, step := range plan.Steps {
		status, readErr := persistence.ReadJSONFile[persistence.StepStatus](store.StepFile(runID, stepIndex, step.ID, persistence.StatusFile))
		if readErr != nil {
			return RunRecord{}, readErr
		}
		stepStatuses = append(stepStatuses, status)
	}
	if err := validateStoredStepStatuses(plan, stepStatuses); err != nil {
		return RunRecord{}, err
	}
	if err := validateStoredSummary(meta, summary, stepStatuses); err != nil {
		return RunRecord{}, err
	}

	return RunRecord{
		RunID:        meta.RunID,
		RunDir:       store.RunDir(runID),
		Meta:         meta,
		Plan:         plan,
		Summary:      summary,
		StepStatuses: stepStatuses,
	}, nil
}

func LoadRunForResume(store persistence.Store, runID string, identity RunIdentity) (RunRecord, error) {
	run, err := LoadRun(store, runID)
	if err != nil {
		return RunRecord{}, err
	}
	if err := ValidateResume(run.Meta, identity); err != nil {
		return RunRecord{}, err
	}

	return run, nil
}

func InitialStepStatuses(plan config.ResolvedPlan) []persistence.StepStatus {
	steps := make([]persistence.StepStatus, 0, len(plan.Steps))
	for index, step := range plan.Steps {
		steps = append(steps, expectedStepStatus(step, index))
	}

	return steps
}

func MarkStaleFromStep(plan config.ResolvedPlan, statuses []persistence.StepStatus, fromStep string) ([]persistence.StepStatus, error) {
	if strings.TrimSpace(fromStep) == "" {
		return nil, fmt.Errorf("from step is required")
	}

	copyPlan := config.ResolvedPlan{
		Env:   cloneStringMap(plan.Env),
		Steps: cloneSteps(plan.Steps),
	}
	copyPlan.ApplyDefaults()
	if err := copyPlan.Validate(); err != nil {
		return nil, fmt.Errorf("validate plan: %w", err)
	}

	affected, err := affectedSteps(copyPlan, fromStep)
	if err != nil {
		return nil, err
	}

	allowedStepIDs := make(map[string]struct{}, len(copyPlan.Steps))
	for _, step := range copyPlan.Steps {
		allowedStepIDs[step.ID] = struct{}{}
	}

	statusByID := make(map[string]persistence.StepStatus, len(statuses))
	for _, status := range statuses {
		if _, allowed := allowedStepIDs[status.StepID]; !allowed {
			return nil, fmt.Errorf("unknown step status %q", status.StepID)
		}
		if _, exists := statusByID[status.StepID]; exists {
			return nil, fmt.Errorf("duplicate step status %q", status.StepID)
		}
		statusByID[status.StepID] = cloneStepStatus(status)
	}

	result := make([]persistence.StepStatus, 0, len(copyPlan.Steps))
	for index, step := range copyPlan.Steps {
		expected := expectedStepStatus(step, index)
		status, exists := statusByID[step.ID]
		if !exists {
			status = expected
		} else if err := validateStepStatusAgainstExpected(status, expected); err != nil {
			return nil, err
		}
		if _, isAffected := affected[step.ID]; isAffected {
			status.State = string(StepStateStale)
		}
		result = append(result, status)
	}

	return result, nil
}

func validatePrepareInputs(store persistence.Store, identity RunIdentity, plan config.ResolvedPlan, githubConfig config.GitHub) error {
	githubConfig = normalizeGitHubConfig(githubConfig)
	storeRoot, err := filepath.Abs(store.RepoRoot)
	if err != nil {
		return fmt.Errorf("resolve store root: %w", err)
	}
	if storeRoot != identity.RepoRoot {
		return fmt.Errorf("store root %q does not match identity repo root %q", storeRoot, identity.RepoRoot)
	}

	configHash, err := HashFile(identity.ConfigPath)
	if err != nil {
		return err
	}
	if configHash != identity.ConfigHash {
		return fmt.Errorf("config hash does not match current config file")
	}

	planHash, err := HashPlan(plan)
	if err != nil {
		return err
	}
	if planHash != identity.PlanHash {
		return fmt.Errorf("plan hash does not match current plan")
	}
	if githubConfig.Enabled && strings.TrimSpace(githubConfig.AggregateContext) == "" {
		return fmt.Errorf("GitHub aggregate context is required when GitHub posting is enabled")
	}

	return nil
}

func normalizeGitHubConfig(githubConfig config.GitHub) config.GitHub {
	if githubConfig.AggregateContext == "" {
		githubConfig.AggregateContext = config.DefaultAggregateContext
	}
	return githubConfig
}

func validateStoredStepStatuses(plan config.ResolvedPlan, statuses []persistence.StepStatus) error {
	if len(statuses) != len(plan.Steps) {
		return fmt.Errorf("stored step status count does not match persisted plan")
	}

	for index, step := range plan.Steps {
		if err := validateStepStatusAgainstExpected(statuses[index], expectedStepStatus(step, index)); err != nil {
			return err
		}
	}

	return nil
}

func validateStepStatusAgainstExpected(actual persistence.StepStatus, expected persistence.StepStatus) error {
	if actual.StepID != expected.StepID {
		return fmt.Errorf("stored step id does not match persisted plan")
	}
	if actual.StepName != expected.StepName {
		return fmt.Errorf("stored step name does not match persisted plan for %q", expected.StepID)
	}
	if actual.Index != expected.Index {
		return fmt.Errorf("stored step index does not match persisted plan for %q", expected.StepID)
	}
	if !sameStrings(actual.Needs, expected.Needs) {
		return fmt.Errorf("stored step dependencies do not match persisted plan for %q", expected.StepID)
	}
	if actual.GitHubContext != expected.GitHubContext {
		return fmt.Errorf("stored step GitHub context does not match persisted plan for %q", expected.StepID)
	}
	for _, field := range []struct {
		name     string
		actual   string
		expected string
	}{
		{name: "stdout log", actual: actual.StdoutLog, expected: expected.StdoutLog},
		{name: "stderr log", actual: actual.StderrLog, expected: expected.StderrLog},
		{name: "combined log", actual: actual.CombinedLog, expected: expected.CombinedLog},
		{name: "output env", actual: actual.OutputEnv, expected: expected.OutputEnv},
	} {
		if field.actual != field.expected {
			return fmt.Errorf("stored %s path does not match persisted plan for %q", field.name, expected.StepID)
		}
	}
	if !isKnownStepState(actual.State) {
		return fmt.Errorf("stored step state %q is not recognized for %q", actual.State, expected.StepID)
	}
	if actual.DurationMillis < 0 {
		return fmt.Errorf("stored step duration must not be negative for %q", expected.StepID)
	}
	if actual.StartedAt == nil && actual.FinishedAt != nil {
		return fmt.Errorf("stored step finish time requires a start time for %q", expected.StepID)
	}
	if actual.StartedAt != nil && actual.FinishedAt != nil {
		if actual.FinishedAt.Before(*actual.StartedAt) {
			return fmt.Errorf("stored step finish time must not be before start time for %q", expected.StepID)
		}
		expectedDuration := actual.FinishedAt.Sub(*actual.StartedAt).Milliseconds()
		if actual.DurationMillis != expectedDuration {
			return fmt.Errorf("stored step duration does not match timestamps for %q", expected.StepID)
		}
	}
	if err := validateStepStateSemantics(actual, expected.StepID); err != nil {
		return err
	}

	return nil
}

func validateStepStateSemantics(status persistence.StepStatus, stepID string) error {
	switch status.State {
	case string(StepStatePending):
		if status.StartedAt != nil || status.FinishedAt != nil || status.DurationMillis != 0 || status.ExitCode != nil {
			return fmt.Errorf("stored pending step state must not contain execution data for %q", stepID)
		}
	case string(StepStateRunning):
		if status.StartedAt == nil {
			return fmt.Errorf("stored running step state requires a start time for %q", stepID)
		}
		if status.FinishedAt != nil || status.DurationMillis != 0 || status.ExitCode != nil {
			return fmt.Errorf("stored running step state must not contain terminal data for %q", stepID)
		}
	case string(StepStateSuccess):
		if status.StartedAt == nil || status.FinishedAt == nil {
			return fmt.Errorf("stored successful step state requires start and finish times for %q", stepID)
		}
		if status.ExitCode == nil || *status.ExitCode != 0 {
			return fmt.Errorf("stored successful step state requires exit code 0 for %q", stepID)
		}
	case string(StepStateFailure):
		if status.StartedAt == nil || status.FinishedAt == nil {
			return fmt.Errorf("stored failed step state requires start and finish times for %q", stepID)
		}
	case string(StepStateSkipped), string(StepStateBlocked):
		if status.StartedAt == nil || status.FinishedAt == nil {
			return fmt.Errorf("stored %s step state requires start and finish times for %q", status.State, stepID)
		}
		if status.ExitCode != nil {
			return fmt.Errorf("stored %s step state must not contain an exit code for %q", status.State, stepID)
		}
	case string(StepStateStale):
		if status.StartedAt == nil && (status.FinishedAt != nil || status.ExitCode != nil || status.DurationMillis != 0) {
			return fmt.Errorf("stored stale step state must keep execution data consistent for %q", stepID)
		}
	}

	return nil
}

func validateStoredSummary(meta persistence.Meta, summary persistence.Summary, statuses []persistence.StepStatus) error {
	expected := buildSummary(meta.RunID, statuses, meta.StartedAt, meta.FinishedAt)
	if summary.RunID != expected.RunID {
		return fmt.Errorf("stored summary run id does not match run metadata")
	}
	if summary.Status != expected.Status {
		return fmt.Errorf("stored summary status does not match step statuses")
	}
	if !sameTime(summary.StartedAt, expected.StartedAt) || !sameTime(summary.FinishedAt, expected.FinishedAt) {
		return fmt.Errorf("stored summary timestamps do not match run metadata")
	}
	if summary.DurationMillis != expected.DurationMillis {
		return fmt.Errorf("stored summary duration does not match run metadata")
	}
	if !sameStepSummaries(summary.Steps, expected.Steps) {
		return fmt.Errorf("stored summary steps do not match step statuses")
	}
	if !sameCounts(summary.Counts, expected.Counts) {
		return fmt.Errorf("stored summary counts do not match step statuses")
	}

	return nil
}

func buildSummary(runID string, statuses []persistence.StepStatus, startedAt *time.Time, finishedAt *time.Time) persistence.Summary {
	stepSummaries := make([]persistence.StepSummary, 0, len(statuses))
	counts := make(map[string]int)
	for _, status := range statuses {
		counts[status.State]++
		stepSummaries = append(stepSummaries, persistence.StepSummary{
			StepID:        status.StepID,
			State:         status.State,
			GitHubContext: status.GitHubContext,
		})
	}

	summary := persistence.Summary{
		RunID:      runID,
		Status:     summarizeRunStatus(counts),
		StartedAt:  startedAt,
		FinishedAt: finishedAt,
		Steps:      stepSummaries,
		Counts:     counts,
	}
	if startedAt != nil && finishedAt != nil {
		summary.DurationMillis = finishedAt.Sub(*startedAt).Milliseconds()
	}

	return summary
}

func summarizeRunStatus(counts map[string]int) string {
	switch {
	case len(counts) == 0:
		return string(StepStateSuccess)
	case counts[string(StepStatePending)] > 0 || counts[string(StepStateRunning)] > 0:
		return runStatusPending
	case counts[string(StepStateFailure)] > 0:
		return string(StepStateFailure)
	case counts[string(StepStateBlocked)] > 0:
		return string(StepStateBlocked)
	case counts[string(StepStateStale)] > 0:
		return string(StepStateStale)
	case counts[string(StepStateSuccess)] > 0:
		return string(StepStateSuccess)
	case counts[string(StepStateSkipped)] > 0:
		return string(StepStateSkipped)
	default:
		return string(StepStateSuccess)
	}
}

// renderSummaryText turns a run directory into a simple jump list.
//
// First principle: when a run fails, the fastest path is not "open every file and
// search around". It is "show me the status, then point me at the exact next logs".
// summary.txt is intentionally plain text so humans and LLMs can read it without a
// second tool.
func renderSummaryText(runDir string, meta persistence.Meta, summary persistence.Summary, statuses []persistence.StepStatus) string {
	var builder strings.Builder
	builder.WriteString("run_id: ")
	builder.WriteString(summary.RunID)
	builder.WriteByte('\n')
	builder.WriteString("status: ")
	builder.WriteString(summary.Status)
	builder.WriteByte('\n')
	builder.WriteString("artifacts: ")
	builder.WriteString(runDir)
	builder.WriteByte('\n')

	if summary.StartedAt != nil {
		builder.WriteString("started: ")
		builder.WriteString(summary.StartedAt.Format(time.RFC3339))
		builder.WriteByte('\n')
	}
	if summary.FinishedAt != nil {
		builder.WriteString("finished: ")
		builder.WriteString(summary.FinishedAt.Format(time.RFC3339))
		builder.WriteByte('\n')
	}
	if summary.StartedAt != nil && summary.FinishedAt != nil {
		builder.WriteString("duration: ")
		builder.WriteString((time.Duration(summary.DurationMillis) * time.Millisecond).String())
		builder.WriteByte('\n')
	}

	builder.WriteString("runner_logs:\n")
	builder.WriteString("- runner: ")
	builder.WriteString(filepath.Join(runDir, persistence.EventsFile))
	builder.WriteByte('\n')
	builder.WriteString("- planner: ")
	builder.WriteString(filepath.Join(runDir, persistence.PlannerLogFile))
	builder.WriteByte('\n')

	builder.WriteString("snapshot:\n")
	builder.WriteString("- head_tree: ")
	builder.WriteString(meta.HeadTreeHash)
	builder.WriteByte('\n')
	builder.WriteString("- worktree_tree: ")
	builder.WriteString(meta.WorktreeTreeHash)
	builder.WriteByte('\n')
	builder.WriteString("- dirty_worktree: ")
	builder.WriteString(fmt.Sprintf("%t", meta.DirtyWorktree))
	builder.WriteByte('\n')
	if meta.GitHubPostingSuppressed != "" {
		builder.WriteString("- github_posting: suppressed (")
		builder.WriteString(meta.GitHubPostingSuppressed)
		builder.WriteString(")\n")
	} else if meta.GitHubEnabled {
		builder.WriteString("- github_posting: enabled\n")
	}

	builder.WriteString("dirty_files:\n")
	if len(meta.DirtyFiles) == 0 {
		builder.WriteString("- none\n")
	} else {
		for _, file := range meta.DirtyFiles {
			builder.WriteString("- [")
			builder.WriteString(string(file.Status))
			builder.WriteString("] ")
			builder.WriteString(file.Path)
			if file.PreviousPath != "" {
				builder.WriteString(" <- ")
				builder.WriteString(file.PreviousPath)
			}
			if file.BlobHash != "" {
				builder.WriteString(" @ ")
				builder.WriteString(file.BlobHash)
			}
			builder.WriteByte('\n')
		}
	}

	builder.WriteString("steps:\n")
	for _, status := range statuses {
		builder.WriteString("- [")
		builder.WriteString(status.State)
		builder.WriteString("] ")
		builder.WriteString(status.StepID)
		if status.ExitCode != nil {
			builder.WriteString(fmt.Sprintf(" (exit=%d)", *status.ExitCode))
		}
		builder.WriteString(" -> ")
		builder.WriteString(filepath.Join(runDir, status.CombinedLog))
		builder.WriteByte('\n')
	}

	builder.WriteString("active_steps:\n")
	wroteActive := false
	for _, status := range statuses {
		if status.State != string(StepStateRunning) {
			continue
		}
		wroteActive = true
		builder.WriteString("- ")
		builder.WriteString(status.StepID)
		builder.WriteString(" -> ")
		builder.WriteString(filepath.Join(runDir, status.CombinedLog))
		builder.WriteByte('\n')
	}
	if !wroteActive {
		builder.WriteString("- none\n")
	}

	builder.WriteString("failure_points:\n")
	wroteFailure := false
	for _, status := range statuses {
		if status.State != string(StepStateFailure) && status.State != string(StepStateBlocked) && status.State != string(StepStateStale) {
			continue
		}
		wroteFailure = true
		builder.WriteString("- ")
		builder.WriteString(status.StepID)
		builder.WriteString(" [")
		builder.WriteString(status.State)
		builder.WriteString("] -> ")
		builder.WriteString(filepath.Join(runDir, status.CombinedLog))
		builder.WriteByte('\n')
	}
	if !wroteFailure {
		builder.WriteString("- none\n")
	}

	return builder.String()
}

func affectedSteps(plan config.ResolvedPlan, fromStep string) (map[string]struct{}, error) {
	reverse := make(map[string][]string, len(plan.Steps))
	known := false
	for _, step := range plan.Steps {
		if step.ID == fromStep {
			known = true
		}
		for _, need := range step.Needs {
			reverse[need] = append(reverse[need], step.ID)
		}
	}
	if !known {
		return nil, fmt.Errorf("unknown step %q", fromStep)
	}

	affected := make(map[string]struct{})
	queue := []string{fromStep}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		if _, seen := affected[current]; seen {
			continue
		}
		affected[current] = struct{}{}
		queue = append(queue, reverse[current]...)
	}

	return affected, nil
}

func expectedStepStatus(step config.Step, index int) persistence.StepStatus {
	return persistence.StepStatus{
		StepID:        step.ID,
		StepName:      step.Name,
		State:         string(StepStatePending),
		Index:         index + 1,
		Needs:         cloneStrings(step.Needs),
		GitHubContext: step.EffectiveGitHubContext(),
		StdoutLog:     persistence.StepRelPath(index, step.ID, persistence.StdoutLog),
		StderrLog:     persistence.StepRelPath(index, step.ID, persistence.StderrLog),
		CombinedLog:   persistence.StepRelPath(index, step.ID, persistence.CombinedLog),
		OutputEnv:     persistence.StepRelPath(index, step.ID, persistence.OutputEnv),
	}
}

func cloneStepStatus(status persistence.StepStatus) persistence.StepStatus {
	copyStatus := status
	copyStatus.Needs = cloneStrings(status.Needs)
	return copyStatus
}

func isKnownStepState(state string) bool {
	switch state {
	case string(StepStatePending), string(StepStateRunning), string(StepStateSuccess), string(StepStateFailure), string(StepStateSkipped), string(StepStateBlocked), string(StepStateStale):
		return true
	default:
		return false
	}
}

func sameTime(left *time.Time, right *time.Time) bool {
	switch {
	case left == nil && right == nil:
		return true
	case left == nil || right == nil:
		return false
	default:
		return left.Equal(*right)
	}
}

func sameStepSummaries(left []persistence.StepSummary, right []persistence.StepSummary) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func sameCounts(left map[string]int, right map[string]int) bool {
	if len(left) != len(right) {
		return false
	}
	for key, leftValue := range left {
		if right[key] != leftValue {
			return false
		}
	}
	return true
}

func sameStrings(left []string, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

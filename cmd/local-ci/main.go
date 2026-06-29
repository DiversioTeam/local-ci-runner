package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/DiversioTeam/local-ci-runner/internal/config"
	"github.com/DiversioTeam/local-ci-runner/internal/engine"
	"github.com/DiversioTeam/local-ci-runner/internal/events"
	ghstatus "github.com/DiversioTeam/local-ci-runner/internal/github"
	"github.com/DiversioTeam/local-ci-runner/internal/gitrepo"
	"github.com/DiversioTeam/local-ci-runner/internal/persistence"
	"github.com/DiversioTeam/local-ci-runner/internal/planner"
	"github.com/DiversioTeam/local-ci-runner/internal/update"
)

var errRunFailed = errors.New("run finished unsuccessfully")

func main() {
	if err := newCLI(os.Stdout, os.Stderr, ".").run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "local-ci:", err)
		if errors.Is(err, errRunFailed) {
			os.Exit(1)
		}
		os.Exit(2)
	}
}

type cli struct {
	stdout    io.Writer
	stderr    io.Writer
	cwd       string
	outStyles styles
	errStyles styles
}

type executionCLIOptions struct {
	configPath string
	noGitHub   bool
}

type runsOptions struct {
	json bool
}

type showOptions struct {
	runID string
	json  bool
}

type publishOptions struct {
	runID string
}

type logsOptions struct {
	runID     string
	json      bool
	runner    bool
	planner   bool
	stepID    string
	stdout    bool
	stderr    bool
	combined  bool
	outputEnv bool
}

type runListEntry struct {
	RunID          string     `json:"run_id"`
	RunDir         string     `json:"run_dir"`
	Status         string     `json:"status"`
	RunnerPID      *int       `json:"runner_pid,omitempty"`
	StartedAt      *time.Time `json:"started_at,omitempty"`
	FinishedAt     *time.Time `json:"finished_at,omitempty"`
	DurationMillis *int64     `json:"duration_millis,omitempty"`
	Error          string     `json:"error,omitempty"`
}

type showJSON struct {
	RunID            string                   `json:"run_id"`
	RunDir           string                   `json:"run_dir"`
	Status           string                   `json:"status"`
	Meta             persistence.Meta         `json:"meta"`
	Summary          persistence.Summary      `json:"summary"`
	Steps            []persistence.StepStatus `json:"steps"`
	RunnerLogPath    string                   `json:"runner_log_path"`
	PlannerLogPath   string                   `json:"planner_log_path"`
	LatestEvent      *events.Event            `json:"latest_event,omitempty"`
	LatestEventError string                   `json:"latest_event_error,omitempty"`
}

type logsJSON struct {
	RunID   string         `json:"run_id"`
	RunDir  string         `json:"run_dir"`
	Source  string         `json:"source"`
	StepID  string         `json:"step_id,omitempty"`
	View    string         `json:"view,omitempty"`
	Path    string         `json:"path,omitempty"`
	Events  []events.Event `json:"events,omitempty"`
	Content string         `json:"content,omitempty"`
}

func newCLI(stdout io.Writer, stderr io.Writer, cwd string) *cli {
	return &cli{
		stdout:    stdout,
		stderr:    stderr,
		cwd:       cwd,
		outStyles: newStyles(stdout),
		errStyles: newStyles(stderr),
	}
}

func (c *cli) maybePrintUpdateNotice() {
	if !isTerminalWriter(c.stderr) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	message, err := update.Checker{}.Notice(ctx)
	if err != nil || message == "" {
		return
	}
	_, _ = fmt.Fprintln(c.stderr, message)
}

func (c *cli) printVersion() {
	_, _ = fmt.Fprintf(c.stdout, "local-ci %s\n", update.Version)
}

func (c *cli) run(args []string) error {
	c.maybePrintUpdateNotice()
	if len(args) == 0 {
		c.printTopHelp()
		return nil
	}
	if isHelpArg(args[0]) {
		c.printTopHelp()
		return nil
	}
	if args[0] == "--version" {
		c.printVersion()
		return nil
	}

	switch args[0] {
	case "version":
		if hasHelpFlag(args[1:]) {
			c.printVersionHelp()
			return nil
		}
		c.printVersion()
		return nil
	case "help":
		return c.helpCommand(args[1:])
	case "manual":
		return c.manualCommand(args[1:])
	case "publish":
		if hasHelpFlag(args[1:]) {
			c.printPublishHelp()
			return nil
		}
		return c.publishCommand(args[1:])
	case "run":
		if hasHelpFlag(args[1:]) {
			c.printRunHelp()
			return nil
		}
		return c.runCommand(args[1:])
	case "resume":
		if hasHelpFlag(args[1:]) {
			c.printResumeHelp()
			return nil
		}
		return c.resumeCommand(args[1:])
	case "runs":
		if hasHelpFlag(args[1:]) {
			c.printRunsHelp()
			return nil
		}
		return c.runsCommand(args[1:])
	case "show":
		if hasHelpFlag(args[1:]) {
			c.printShowHelp()
			return nil
		}
		return c.showCommand(args[1:])
	case "logs":
		if hasHelpFlag(args[1:]) {
			c.printLogsHelp()
			return nil
		}
		return c.logsCommand(args[1:])
	default:
		return fmt.Errorf("unknown command %q (run 'local-ci --help')", args[0])
	}
}

func (c *cli) helpCommand(args []string) error {
	if len(args) == 0 {
		c.printTopHelp()
		return nil
	}
	if len(args) != 1 {
		return fmt.Errorf("help accepts at most one command")
	}

	switch args[0] {
	case "all", "manual":
		return c.manualCommand(nil)
	case "publish":
		c.printPublishHelp()
	case "version":
		c.printVersionHelp()
	case "run":
		c.printRunHelp()
	case "resume":
		c.printResumeHelp()
	case "runs":
		c.printRunsHelp()
	case "show":
		c.printShowHelp()
	case "logs":
		c.printLogsHelp()
	default:
		return fmt.Errorf("unknown help topic %q", args[0])
	}
	return nil
}

func (c *cli) runCommand(args []string) error {
	opts, err := parseExecutionArgs(args, false)
	if err != nil {
		return err
	}

	ctx := context.Background()
	repo, cfg, plan, plannerLog, identity, store, err := prepareContext(ctx, c.cwd, opts.configPath)
	if err != nil {
		return err
	}
	githubPostingSuppressed := suppressedGitHubPostingReason("", repo.DirtyWorktree, opts.noGitHub, cfg.GitHub.Enabled)
	runRecord, err := engine.PrepareRun(store, engine.PrepareOptions{
		Identity:                identity,
		Plan:                    plan,
		GitHub:                  cfg.GitHub,
		PlannerLog:              plannerLog,
		HeadTreeHash:            repo.HeadTreeHash,
		WorktreeTreeHash:        repo.WorktreeTreeHash,
		DirtyWorktree:           repo.DirtyWorktree,
		DirtyFiles:              toPersistedWorktreeFiles(repo.DirtyFiles),
		GitHubPostingSuppressed: githubPostingSuppressed,
		Random:                  rand.Reader,
	})
	if err != nil {
		return err
	}

	return c.executeAndReport(ctx, repo, cfg, store, runRecord, opts.noGitHub)
}

func (c *cli) resumeCommand(args []string) error {
	opts, runID, err := parseExecutionAndRunIDArgs(args)
	if err != nil {
		return err
	}

	ctx := context.Background()
	repo, cfg, _, _, identity, store, err := prepareContext(ctx, c.cwd, opts.configPath)
	if err != nil {
		return err
	}
	runRecord, err := engine.LoadRunForResume(store, runID, identity)
	if err != nil {
		return err
	}
	runRecord.Meta.GitHubPostingSuppressed = suppressedGitHubPostingReason(runRecord.Meta.GitHubPostingSuppressed, repo.DirtyWorktree, opts.noGitHub, cfg.GitHub.Enabled)

	return c.executeAndReport(ctx, repo, cfg, store, runRecord, opts.noGitHub)
}

func (c *cli) publishCommand(args []string) error {
	opts, err := parsePublishArgs(args)
	if err != nil {
		return err
	}

	repoRoot, store, err := c.readOnlyStore()
	if err != nil {
		return err
	}
	run, err := engine.LoadRun(store, opts.runID)
	if err != nil {
		return err
	}
	ctx := context.Background()
	repo, _, _, _, identity, _, err := prepareContext(ctx, repoRoot, run.Meta.ConfigPath)
	if err != nil {
		return err
	}
	if err := validatePublishableRun(repo, identity, run); err != nil {
		return err
	}
	if err := engine.PublishCompletedRun(store, run, engine.PublishOptions{
		Context:   ctx,
		Reporter:  ghstatus.CLIReporter{Token: os.Getenv(ghstatus.TokenEnvVar)},
		TargetSHA: repo.HeadSHA,
	}); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(c.stdout, "published run %s to %s @ %s\n", run.RunID, repo.RepoSlug, repo.HeadSHA)
	return nil
}

func (c *cli) runsCommand(args []string) error {
	opts, err := parseRunsArgs(args)
	if err != nil {
		return err
	}

	_, store, err := c.readOnlyStore()
	if err != nil {
		return err
	}
	runIDs, err := store.ListRunIDs()
	if err != nil {
		return err
	}

	entries := make([]runListEntry, 0, len(runIDs))
	for _, runID := range runIDs {
		entry := runListEntry{RunID: runID, RunDir: store.RunDir(runID)}
		run, loadErr := loadRunForInspect(store, runID)
		if loadErr != nil {
			entry.Status = "error"
			entry.Error = loadErr.Error()
			entries = append(entries, entry)
			continue
		}
		entry.Status = displayRunStatus(run.Summary.Status, run.StepStatuses)
		entry.RunnerPID = displayRunnerPID(run.Meta)
		entry.StartedAt = run.Meta.StartedAt
		entry.FinishedAt = run.Meta.FinishedAt
		entry.DurationMillis = durationPtr(run.Summary.DurationMillis, run.Meta.StartedAt, run.Meta.FinishedAt)
		entries = append(entries, entry)
	}

	if opts.json {
		return writeJSON(c.stdout, entries)
	}

	if len(entries) == 0 {
		_, _ = fmt.Fprintf(c.stdout, "no runs under %s\n", store.RunsRoot())
		return nil
	}

	_, _ = fmt.Fprintf(c.stdout, "%-26s %-9s %-8s %-20s %-20s %s\n", "RUN ID", "STATUS", "PID", "STARTED", "FINISHED", "DURATION")
	for _, entry := range entries {
		statusText := entry.Status
		if entry.Error != "" {
			statusText = "error"
		}
		statusField := fmt.Sprintf("%-9s", statusText)
		statusField = c.outStyles.status(statusField, statusText)
		_, _ = fmt.Fprintf(
			c.stdout,
			"%-26s %s %-8s %-20s %-20s %s",
			entry.RunID,
			statusField,
			formatPID(entry.RunnerPID),
			formatTime(entry.StartedAt),
			formatTime(entry.FinishedAt),
			formatDurationField(entry.DurationMillis),
		)
		if entry.Error != "" {
			_, _ = fmt.Fprintf(c.stdout, "  %s", entry.Error)
		}
		_, _ = fmt.Fprintln(c.stdout)
	}

	return nil
}

func (c *cli) showCommand(args []string) error {
	opts, err := parseShowArgs(args)
	if err != nil {
		return err
	}

	run, store, err := c.loadRun(opts.runID)
	if err != nil {
		return err
	}
	runnerLogPath := store.RunFile(run.RunID, persistence.EventsFile)
	plannerLogPath := store.RunFile(run.RunID, persistence.PlannerLogFile)
	eventItems, eventErr := events.ReadFile(runnerLogPath)
	status := displayRunStatus(run.Summary.Status, run.StepStatuses)

	if opts.json {
		payload := showJSON{
			RunID:          run.RunID,
			RunDir:         run.RunDir,
			Status:         status,
			Meta:           run.Meta,
			Summary:        run.Summary,
			Steps:          run.StepStatuses,
			RunnerLogPath:  runnerLogPath,
			PlannerLogPath: plannerLogPath,
		}
		if len(eventItems) > 0 {
			payload.LatestEvent = &eventItems[len(eventItems)-1]
		}
		if eventErr != nil {
			payload.LatestEventError = eventErr.Error()
		}
		return writeJSON(c.stdout, payload)
	}

	_, _ = fmt.Fprintf(c.stdout, "run: %s\n", run.RunID)
	_, _ = fmt.Fprintf(c.stdout, "status: %s\n", c.outStyles.status(status, status))
	_, _ = fmt.Fprintf(c.stdout, "repo: %s @ %s\n", run.Meta.RepoSlug, run.Meta.HeadSHA)
	_, _ = fmt.Fprintf(c.stdout, "artifacts: %s\n", run.RunDir)
	_, _ = fmt.Fprintf(c.stdout, "started: %s\n", formatTime(run.Meta.StartedAt))
	if pid := displayRunnerPID(run.Meta); pid != nil {
		_, _ = fmt.Fprintf(c.stdout, "pid: %d\n", *pid)
	}
	_, _ = fmt.Fprintf(c.stdout, "snapshot:\n  head_tree: %s\n  worktree_tree: %s\n  dirty_worktree: %t\n", run.Meta.HeadTreeHash, run.Meta.WorktreeTreeHash, run.Meta.DirtyWorktree)
	if run.Meta.GitHubPostingSuppressed != "" {
		_, _ = fmt.Fprintf(c.stdout, "  github_posting: suppressed (%s)\n", run.Meta.GitHubPostingSuppressed)
	}
	if run.Meta.FinishedAt != nil {
		_, _ = fmt.Fprintf(c.stdout, "finished: %s\n", formatTime(run.Meta.FinishedAt))
		_, _ = fmt.Fprintf(c.stdout, "duration: %s\n", formatDurationField(durationPtr(run.Summary.DurationMillis, run.Meta.StartedAt, run.Meta.FinishedAt)))
	}
	if counts := formatCounts(run.Summary.Counts); counts != "" {
		_, _ = fmt.Fprintf(c.stdout, "counts: %s\n", counts)
	}
	if len(eventItems) > 0 {
		last := eventItems[len(eventItems)-1]
		_, _ = fmt.Fprintf(c.stdout, "latest_event: %s %s\n", last.Time.Format(time.RFC3339), runnerEventText(last))
	} else if eventErr != nil {
		_, _ = fmt.Fprintf(c.stdout, "latest_event: unavailable (%s)\n", eventErr)
	}
	_, _ = fmt.Fprintf(c.stdout, "runner_logs:\n  runner: %s\n  planner: %s\n", runnerLogPath, plannerLogPath)

	_, _ = fmt.Fprintln(c.stdout, "dirty_files:")
	if len(run.Meta.DirtyFiles) == 0 {
		_, _ = fmt.Fprintln(c.stdout, "- none")
	} else {
		for _, file := range run.Meta.DirtyFiles {
			_, _ = fmt.Fprintf(c.stdout, "- [%s] %s", file.Status, file.Path)
			if file.PreviousPath != "" {
				_, _ = fmt.Fprintf(c.stdout, " <- %s", file.PreviousPath)
			}
			if file.BlobHash != "" {
				_, _ = fmt.Fprintf(c.stdout, " @ %s", file.BlobHash)
			}
			_, _ = fmt.Fprintln(c.stdout)
		}
	}

	renderShowSection(c.stdout, "active_steps", run.RunDir, run.StepStatuses, isRunningStep, c.outStyles)
	renderShowSection(c.stdout, "failure_points", run.RunDir, run.StepStatuses, isFailurePoint, c.outStyles)

	_, _ = fmt.Fprintln(c.stdout, "steps:")
	for _, statusItem := range run.StepStatuses {
		_, _ = fmt.Fprintf(c.stdout, "- [%s] %s", c.outStyles.status(statusItem.State, statusItem.State), statusItem.StepID)
		if statusItem.StepName != "" && statusItem.StepName != statusItem.StepID {
			_, _ = fmt.Fprintf(c.stdout, " (%s)", statusItem.StepName)
		}
		if statusItem.ExitCode != nil {
			_, _ = fmt.Fprintf(c.stdout, " exit=%d", *statusItem.ExitCode)
		}
		if statusItem.StartedAt != nil && statusItem.FinishedAt != nil {
			_, _ = fmt.Fprintf(c.stdout, " duration=%s", time.Duration(statusItem.DurationMillis)*time.Millisecond)
		} else if statusItem.State == string(engine.StepStateRunning) && statusItem.StartedAt != nil {
			_, _ = fmt.Fprintf(c.stdout, " started=%s", statusItem.StartedAt.Format(time.RFC3339))
		}
		_, _ = fmt.Fprintln(c.stdout)
	}

	return nil
}

func (c *cli) logsCommand(args []string) error {
	opts, err := parseLogsArgs(args)
	if err != nil {
		return err
	}
	source, view, err := resolveLogSelection(opts)
	if err != nil {
		return err
	}

	_, store, err := c.readOnlyStore()
	if err != nil {
		return err
	}
	runDir := store.RunDir(opts.runID)

	switch source {
	case "runner":
		path := store.RunFile(opts.runID, persistence.EventsFile)
		items, err := events.ReadFile(path)
		if err != nil {
			return err
		}
		if opts.json {
			return writeJSON(c.stdout, logsJSON{
				RunID:  opts.runID,
				RunDir: runDir,
				Source: source,
				Path:   path,
				Events: items,
			})
		}
		for _, item := range items {
			_, _ = fmt.Fprintln(c.stdout, c.outStyles.runnerEvent(item))
		}
		return nil
	case "planner":
		path := store.RunFile(opts.runID, persistence.PlannerLogFile)
		return c.writeLogFile(opts.runID, runDir, source, view, path, "", opts.json)
	case "step":
		run, _, err := c.loadRun(opts.runID)
		if err != nil {
			return err
		}
		statusItem, ok := findStepStatus(run.StepStatuses, opts.stepID)
		if !ok {
			return fmt.Errorf("unknown step %q", opts.stepID)
		}
		path := stepLogPath(run.RunDir, statusItem, view)
		return c.writeLogFile(run.RunID, run.RunDir, source, view, path, opts.stepID, opts.json)
	default:
		return fmt.Errorf("unknown log source %q", source)
	}
}

func (c *cli) writeLogFile(runID string, runDir string, source string, view string, path string, stepID string, asJSON bool) error {
	payload, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if asJSON {
		return writeJSON(c.stdout, logsJSON{
			RunID:   runID,
			RunDir:  runDir,
			Source:  source,
			StepID:  stepID,
			View:    view,
			Path:    path,
			Content: string(payload),
		})
	}
	_, err = c.stdout.Write(payload)
	return err
}

func (c *cli) loadRun(runID string) (engine.RunRecord, persistence.Store, error) {
	_, store, err := c.readOnlyStore()
	if err != nil {
		return engine.RunRecord{}, persistence.Store{}, err
	}
	run, err := loadRunForInspect(store, runID)
	if err != nil {
		return engine.RunRecord{}, persistence.Store{}, err
	}
	return run, store, nil
}

// loadRunForInspect keeps operator UX tolerant while a run is still writing files.
//
// First principle: the engine should be strict when resuming work, but humans asking
// "what is happening right now?" should get the freshest safe snapshot we can build.
// During an active run the writer can briefly update status.json before summary.json,
// which is valid on disk but can make the strict loader reject that instant in time.
//
// So inspection does three things:
//  1. try the strict loader first
//  2. retry a couple of times because the writer may finish the matching summary write
//  3. fall back to a best-effort snapshot built from meta + plan + step statuses
//
// Resume still uses engine.LoadRunForResume. This fallback is for read-only inspection
// commands only.
func loadRunForInspect(store persistence.Store, runID string) (engine.RunRecord, error) {
	var lastErr error
	for range 3 {
		run, err := engine.LoadRun(store, runID)
		if err == nil {
			return run, nil
		}
		lastErr = err
		if !isSummaryDriftError(err) {
			return engine.RunRecord{}, err
		}
		time.Sleep(10 * time.Millisecond)
	}

	run, err := loadRunBestEffort(store, runID)
	if err != nil {
		return engine.RunRecord{}, lastErr
	}
	return run, nil
}

// loadRunBestEffort intentionally rebuilds the run summary from the files that move
// most often during execution. This gives show/runs a stable read model for active
// runs without introducing a second persisted state format.
func loadRunBestEffort(store persistence.Store, runID string) (engine.RunRecord, error) {
	meta, err := persistence.ReadJSONFile[persistence.Meta](store.RunFile(runID, persistence.MetaFile))
	if err != nil {
		return engine.RunRecord{}, err
	}
	if meta.RunID != runID {
		return engine.RunRecord{}, fmt.Errorf("stored run id does not match requested run directory")
	}

	plan, err := persistence.ReadJSONFile[config.ResolvedPlan](store.RunFile(runID, persistence.PlanFile))
	if err != nil {
		return engine.RunRecord{}, err
	}
	plan.ApplyDefaults()

	stepStatuses := make([]persistence.StepStatus, 0, len(plan.Steps))
	for stepIndex, step := range plan.Steps {
		status, readErr := persistence.ReadJSONFile[persistence.StepStatus](store.StepFile(runID, stepIndex, step.ID, persistence.StatusFile))
		if readErr != nil {
			return engine.RunRecord{}, readErr
		}
		stepStatuses = append(stepStatuses, status)
	}

	summary := buildInspectSummary(runID, stepStatuses, meta.StartedAt, meta.FinishedAt)
	if storedSummary, readErr := persistence.ReadJSONFile[persistence.Summary](store.RunFile(runID, persistence.SummaryFile)); readErr == nil {
		summary.Metadata = cloneStringMap(storedSummary.Metadata)
	}

	return engine.RunRecord{
		RunID:        runID,
		RunDir:       store.RunDir(runID),
		Meta:         meta,
		Plan:         plan,
		Summary:      summary,
		StepStatuses: stepStatuses,
	}, nil
}

// buildInspectSummary mirrors the engine's summary rules so JSON and plain-text
// inspection still reflect the same underlying persisted facts.
func buildInspectSummary(runID string, statuses []persistence.StepStatus, startedAt *time.Time, finishedAt *time.Time) persistence.Summary {
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
		Status:     summarizeInspectRunStatus(counts),
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

func summarizeInspectRunStatus(counts map[string]int) string {
	switch {
	case len(counts) == 0:
		return string(engine.StepStateSuccess)
	case counts[string(engine.StepStatePending)] > 0 || counts[string(engine.StepStateRunning)] > 0:
		return string(engine.StepStatePending)
	case counts[string(engine.StepStateFailure)] > 0:
		return string(engine.StepStateFailure)
	case counts[string(engine.StepStateBlocked)] > 0:
		return string(engine.StepStateBlocked)
	case counts[string(engine.StepStateStale)] > 0:
		return string(engine.StepStateStale)
	case counts[string(engine.StepStateSuccess)] > 0:
		return string(engine.StepStateSuccess)
	case counts[string(engine.StepStateSkipped)] > 0:
		return string(engine.StepStateSkipped)
	default:
		return string(engine.StepStateSuccess)
	}
}

func isSummaryDriftError(err error) bool {
	return strings.Contains(err.Error(), "stored summary ")
}

func cloneStringMap(src map[string]string) map[string]string {
	if src == nil {
		return nil
	}
	copyMap := make(map[string]string, len(src))
	for key, value := range src {
		copyMap[key] = value
	}
	return copyMap
}

func toPersistedWorktreeFiles(src []gitrepo.WorktreeFile) []persistence.WorktreeFile {
	if src == nil {
		return nil
	}
	dst := make([]persistence.WorktreeFile, len(src))
	for index, item := range src {
		dst[index] = persistence.WorktreeFile{
			Path:         item.Path,
			Status:       persistence.WorktreeFileStatus(item.Status),
			PreviousPath: item.PreviousPath,
			BlobHash:     item.BlobHash,
		}
	}
	return dst
}

// validatePublishableRun answers the trust question for delayed GitHub posting.
//
// A completed dirty-worktree run can only be published later when the current
// clean checkout still represents the exact same code and plan snapshot.
// Otherwise we would be attaching an old local result to new code.
func validatePublishableRun(repo gitrepo.Info, identity engine.RunIdentity, run engine.RunRecord) error {
	if run.Meta.RepoRoot != repo.Root {
		return fmt.Errorf("publish refused: repo root changed")
	}
	if run.Meta.RepoSlug != repo.RepoSlug {
		return fmt.Errorf("publish refused: repo slug changed")
	}
	if run.Meta.ConfigPath != identity.ConfigPath {
		return fmt.Errorf("publish refused: config path changed")
	}
	if run.Meta.ConfigHash != identity.ConfigHash {
		return fmt.Errorf("publish refused: config hash changed")
	}
	if run.Meta.PlanHash != identity.PlanHash {
		return fmt.Errorf("publish refused: plan hash changed")
	}
	if run.Meta.FinishedAt == nil || run.Summary.Status == "pending" {
		return fmt.Errorf("publish refused: run %s has not finished", run.RunID)
	}
	if !run.Meta.GitHubEnabled {
		return fmt.Errorf("publish refused: GitHub posting was disabled for this run")
	}
	if strings.TrimSpace(run.Meta.GitHubPostingSuppressed) == "" {
		return fmt.Errorf("publish refused: run %s already posted during execution", run.RunID)
	}
	if repo.DirtyWorktree {
		return fmt.Errorf("publish refused: current worktree is dirty")
	}
	if strings.TrimSpace(run.Meta.WorktreeTreeHash) == "" {
		return fmt.Errorf("publish refused: run %s does not record a worktree snapshot", run.RunID)
	}
	if repo.HeadTreeHash != repo.WorktreeTreeHash {
		return fmt.Errorf("publish refused: current HEAD tree %s does not match current worktree %s", repo.HeadTreeHash, repo.WorktreeTreeHash)
	}
	if repo.HeadTreeHash != run.Meta.WorktreeTreeHash {
		return fmt.Errorf("publish refused: current HEAD tree %s does not match stored run snapshot %s", repo.HeadTreeHash, run.Meta.WorktreeTreeHash)
	}
	return nil
}

func (c *cli) readOnlyStore() (string, persistence.Store, error) {
	repoRoot, err := gitrepo.DiscoverRoot(context.Background(), c.cwd)
	if err != nil {
		return "", persistence.Store{}, err
	}
	return repoRoot, persistence.NewStore(repoRoot), nil
}

func prepareContext(
	ctx context.Context,
	startDir string,
	configPath string,
) (gitrepo.Info, config.File, config.ResolvedPlan, string, engine.RunIdentity, persistence.Store, error) {
	repo, err := gitrepo.Discover(ctx, startDir)
	if err != nil {
		return gitrepo.Info{}, config.File{}, config.ResolvedPlan{}, "", engine.RunIdentity{}, persistence.Store{}, err
	}
	resolvedConfigPath := resolveConfigPath(repo.Root, configPath)
	cfg, err := config.Load(resolvedConfigPath)
	if err != nil {
		return gitrepo.Info{}, config.File{}, config.ResolvedPlan{}, "", engine.RunIdentity{}, persistence.Store{}, err
	}

	var (
		plan       config.ResolvedPlan
		plannerLog string
	)
	if cfg.Planner != nil {
		result, plannerErr := planner.Execute(ctx, repo.Root, repo.RepoSlug, repo.HeadSHA, resolvedConfigPath, *cfg.Planner)
		if plannerErr != nil {
			return gitrepo.Info{}, config.File{}, config.ResolvedPlan{}, "", engine.RunIdentity{}, persistence.Store{}, plannerErr
		}
		plan = result.Plan
		plannerLog = result.Log
	} else {
		plan = cfg.StaticPlan()
	}

	identity, err := engine.BuildRunIdentity(repo.Root, repo.RepoSlug, repo.HeadSHA, resolvedConfigPath, plan)
	if err != nil {
		return gitrepo.Info{}, config.File{}, config.ResolvedPlan{}, "", engine.RunIdentity{}, persistence.Store{}, err
	}
	identity.WorktreeTreeHash = repo.WorktreeTreeHash
	store := persistence.NewStore(repo.Root)
	return repo, cfg, plan, plannerLog, identity, store, nil
}

func (c *cli) executeAndReport(
	ctx context.Context,
	repo gitrepo.Info,
	cfg config.File,
	store persistence.Store,
	runRecord engine.RunRecord,
	noGitHub bool,
) error {
	executed, err := engine.ExecuteRun(store, runRecord, engine.ExecuteOptions{
		Context:  ctx,
		Reporter: newReporter(cfg, noGitHub),
		Stdout:   c.stdout,
		Stderr:   c.stderr,
		Progress: newProgressWriter(c.stderr, c.errStyles),
	})
	if err != nil {
		return err
	}

	_, _ = fmt.Fprintf(c.stdout, "run %s: %s\n", executed.RunID, c.outStyles.status(executed.Summary.Status, executed.Summary.Status))
	_, _ = fmt.Fprintf(c.stdout, "repo: %s @ %s\n", repo.RepoSlug, repo.HeadSHA)
	_, _ = fmt.Fprintf(c.stdout, "artifacts: %s\n", executed.RunDir)
	switch executed.Meta.GitHubPostingSuppressed {
	case "dirty_worktree":
		_, _ = fmt.Fprintln(c.stdout, "github: skipped for dirty worktree; commit the same snapshot and use local-ci publish <run-id>")
	case "cli_disabled":
		_, _ = fmt.Fprintln(c.stdout, "github: disabled by --no-github; use local-ci publish <run-id> if you want to post this result later")
	}

	if failedSummary(executed.Summary.Status) {
		return errRunFailed
	}
	return nil
}

func newReporter(cfg config.File, noGitHub bool) ghstatus.Reporter {
	if !cfg.GitHub.Enabled || noGitHub {
		return nil
	}
	return ghstatus.CLIReporter{Token: os.Getenv(ghstatus.TokenEnvVar)}
}

func resolveConfigPath(repoRoot string, configPath string) string {
	if filepath.IsAbs(configPath) {
		return configPath
	}
	return filepath.Join(repoRoot, configPath)
}

func failedSummary(summaryStatus string) bool {
	switch summaryStatus {
	case string(engine.StepStateSuccess), string(engine.StepStateSkipped):
		return false
	default:
		return true
	}
}

func parseExecutionArgs(args []string, allowPositional bool) (executionCLIOptions, error) {
	opts := executionCLIOptions{configPath: config.DefaultPath}
	positionals := make([]string, 0)
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case strings.HasPrefix(arg, "--config="):
			value := strings.TrimPrefix(arg, "--config=")
			if err := requireFlagValue("--config", value); err != nil {
				return executionCLIOptions{}, err
			}
			opts.configPath = value
		case arg == "--config":
			index++
			if index >= len(args) {
				return executionCLIOptions{}, fmt.Errorf("--config requires a value")
			}
			if err := requireFlagValue("--config", args[index]); err != nil {
				return executionCLIOptions{}, err
			}
			opts.configPath = args[index]
		case arg == "--no-github":
			opts.noGitHub = true
		case strings.HasPrefix(arg, "-"):
			return executionCLIOptions{}, fmt.Errorf("unknown flag %q", arg)
		default:
			positionals = append(positionals, arg)
		}
	}
	if !allowPositional && len(positionals) > 0 {
		return executionCLIOptions{}, fmt.Errorf("unexpected positional arguments: %s", strings.Join(positionals, " "))
	}
	return opts, nil
}

func parseExecutionAndRunIDArgs(args []string) (executionCLIOptions, string, error) {
	opts, err := parseExecutionArgs(args, true)
	if err != nil {
		return executionCLIOptions{}, "", err
	}
	positionals := collectExecutionPositionals(args)
	if len(positionals) != 1 {
		return executionCLIOptions{}, "", fmt.Errorf("resume requires exactly one run id")
	}
	return opts, positionals[0], nil
}

func parseRunsArgs(args []string) (runsOptions, error) {
	var opts runsOptions
	for _, arg := range args {
		switch arg {
		case "--json":
			opts.json = true
		default:
			if strings.HasPrefix(arg, "-") {
				return runsOptions{}, fmt.Errorf("unknown flag %q", arg)
			}
			return runsOptions{}, fmt.Errorf("runs does not accept positional arguments")
		}
	}
	return opts, nil
}

func parseShowArgs(args []string) (showOptions, error) {
	var opts showOptions
	positionals := make([]string, 0, 1)
	for _, arg := range args {
		switch arg {
		case "--json":
			opts.json = true
		default:
			if strings.HasPrefix(arg, "-") {
				return showOptions{}, fmt.Errorf("unknown flag %q", arg)
			}
			positionals = append(positionals, arg)
		}
	}
	if len(positionals) != 1 {
		return showOptions{}, fmt.Errorf("show requires exactly one run id")
	}
	opts.runID = positionals[0]
	return opts, nil
}

func parsePublishArgs(args []string) (publishOptions, error) {
	positionals := make([]string, 0, 1)
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") {
			return publishOptions{}, fmt.Errorf("unknown flag %q", arg)
		}
		positionals = append(positionals, arg)
	}
	if len(positionals) != 1 {
		return publishOptions{}, fmt.Errorf("publish requires exactly one run id")
	}
	return publishOptions{runID: positionals[0]}, nil
}

func parseLogsArgs(args []string) (logsOptions, error) {
	var opts logsOptions
	positionals := make([]string, 0, 1)
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			opts.json = true
		case arg == "--runner":
			opts.runner = true
		case arg == "--planner":
			opts.planner = true
		case strings.HasPrefix(arg, "--step="):
			value := strings.TrimPrefix(arg, "--step=")
			if err := requireFlagValue("--step", value); err != nil {
				return logsOptions{}, err
			}
			opts.stepID = value
		case arg == "--step":
			index++
			if index >= len(args) {
				return logsOptions{}, fmt.Errorf("--step requires a value")
			}
			if err := requireFlagValue("--step", args[index]); err != nil {
				return logsOptions{}, err
			}
			opts.stepID = args[index]
		case arg == "--stdout":
			opts.stdout = true
		case arg == "--stderr":
			opts.stderr = true
		case arg == "--combined":
			opts.combined = true
		case arg == "--output-env":
			opts.outputEnv = true
		default:
			if strings.HasPrefix(arg, "-") {
				return logsOptions{}, fmt.Errorf("unknown flag %q", arg)
			}
			positionals = append(positionals, arg)
		}
	}
	if len(positionals) != 1 {
		return logsOptions{}, fmt.Errorf("logs requires exactly one run id")
	}
	opts.runID = positionals[0]
	return opts, nil
}

func resolveLogSelection(opts logsOptions) (string, string, error) {
	sources := 0
	if opts.runner {
		sources++
	}
	if opts.planner {
		sources++
	}
	if opts.stepID != "" {
		sources++
	}
	if sources > 1 {
		return "", "", fmt.Errorf("choose exactly one log source: --runner, --planner, or --step <step-id>")
	}

	views := 0
	view := ""
	if opts.stdout {
		views++
		view = "stdout"
	}
	if opts.stderr {
		views++
		view = "stderr"
	}
	if opts.combined {
		views++
		view = "combined"
	}
	if opts.outputEnv {
		views++
		view = "output-env"
	}
	if views > 1 {
		return "", "", fmt.Errorf("choose at most one step log view: --stdout, --stderr, --combined, or --output-env")
	}
	if opts.stepID == "" && views > 0 {
		return "", "", fmt.Errorf("step log views require --step <step-id>")
	}
	if opts.planner {
		return "planner", "", nil
	}
	if opts.stepID != "" {
		if view == "" {
			view = "combined"
		}
		return "step", view, nil
	}
	return "runner", "", nil
}

func collectExecutionPositionals(args []string) []string {
	positionals := make([]string, 0)
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--config":
			index++
		case strings.HasPrefix(arg, "--config="):
		case arg == "--no-github":
		case strings.HasPrefix(arg, "-"):
		default:
			positionals = append(positionals, arg)
		}
	}
	return positionals
}

func requireFlagValue(flag string, value string) error {
	if strings.TrimSpace(value) == "" || strings.HasPrefix(value, "-") {
		return fmt.Errorf("%s requires a value", flag)
	}
	return nil
}

func suppressedGitHubPostingReason(existing string, dirtyWorktree bool, noGitHub bool, githubEnabled bool) string {
	if !githubEnabled {
		return ""
	}
	if noGitHub {
		return "cli_disabled"
	}
	if existing != "" {
		return existing
	}
	if dirtyWorktree {
		return "dirty_worktree"
	}
	return ""
}

func isHelpArg(arg string) bool {
	return arg == "-h" || arg == "--help"
}

func hasHelpFlag(args []string) bool {
	for _, arg := range args {
		if isHelpArg(arg) {
			return true
		}
	}
	return false
}

func durationPtr(durationMillis int64, startedAt *time.Time, finishedAt *time.Time) *int64 {
	if startedAt == nil || finishedAt == nil {
		return nil
	}
	value := durationMillis
	return &value
}

func formatTime(value *time.Time) string {
	if value == nil {
		return "-"
	}
	return value.Format(time.RFC3339)
}

func formatDurationField(durationMillis *int64) string {
	if durationMillis == nil {
		return "-"
	}
	return (time.Duration(*durationMillis) * time.Millisecond).String()
}

func formatPID(pid *int) string {
	if pid == nil {
		return "-"
	}
	return fmt.Sprintf("%d", *pid)
}

func displayRunnerPID(meta persistence.Meta) *int {
	if meta.FinishedAt != nil {
		return nil
	}
	return meta.RunnerPID
}

func displayRunStatus(summaryStatus string, statuses []persistence.StepStatus) string {
	for _, statusItem := range statuses {
		if statusItem.State == string(engine.StepStateRunning) {
			return string(engine.StepStateRunning)
		}
	}
	return summaryStatus
}

func findStepStatus(statuses []persistence.StepStatus, stepID string) (persistence.StepStatus, bool) {
	for _, statusItem := range statuses {
		if statusItem.StepID == stepID {
			return statusItem, true
		}
	}
	return persistence.StepStatus{}, false
}

func stepLogPath(runDir string, statusItem persistence.StepStatus, view string) string {
	switch view {
	case "stdout":
		return filepath.Join(runDir, statusItem.StdoutLog)
	case "stderr":
		return filepath.Join(runDir, statusItem.StderrLog)
	case "output-env":
		return filepath.Join(runDir, statusItem.OutputEnv)
	default:
		return filepath.Join(runDir, statusItem.CombinedLog)
	}
}

func formatCounts(counts map[string]int) string {
	if len(counts) == 0 {
		return ""
	}
	ordered := []string{
		string(engine.StepStateRunning),
		string(engine.StepStatePending),
		string(engine.StepStateFailure),
		string(engine.StepStateBlocked),
		string(engine.StepStateStale),
		string(engine.StepStateSkipped),
		string(engine.StepStateSuccess),
	}
	parts := make([]string, 0, len(counts))
	seen := make(map[string]struct{}, len(counts))
	for _, key := range ordered {
		if counts[key] == 0 {
			continue
		}
		seen[key] = struct{}{}
		parts = append(parts, fmt.Sprintf("%s=%d", key, counts[key]))
	}
	extra := make([]string, 0)
	for key := range counts {
		if _, ok := seen[key]; ok || counts[key] == 0 {
			continue
		}
		extra = append(extra, key)
	}
	sort.Strings(extra)
	for _, key := range extra {
		parts = append(parts, fmt.Sprintf("%s=%d", key, counts[key]))
	}
	return strings.Join(parts, ", ")
}

func renderShowSection(
	writer io.Writer,
	heading string,
	runDir string,
	statuses []persistence.StepStatus,
	keep func(persistence.StepStatus) bool,
	out styles,
) {
	_, _ = fmt.Fprintf(writer, "%s:\n", heading)
	count := 0
	for _, statusItem := range statuses {
		if !keep(statusItem) {
			continue
		}
		count++
		_, _ = fmt.Fprintf(writer, "- [%s] %s\n", out.status(statusItem.State, statusItem.State), statusItem.StepID)
		_, _ = fmt.Fprintf(writer, "  combined: %s\n", filepath.Join(runDir, statusItem.CombinedLog))
		_, _ = fmt.Fprintf(writer, "  stdout: %s\n", filepath.Join(runDir, statusItem.StdoutLog))
		_, _ = fmt.Fprintf(writer, "  stderr: %s\n", filepath.Join(runDir, statusItem.StderrLog))
	}
	if count == 0 {
		_, _ = fmt.Fprintln(writer, "- none")
	}
}

func isRunningStep(statusItem persistence.StepStatus) bool {
	return statusItem.State == string(engine.StepStateRunning)
}

func isFailurePoint(statusItem persistence.StepStatus) bool {
	switch statusItem.State {
	case string(engine.StepStateFailure), string(engine.StepStateBlocked), string(engine.StepStateStale):
		return true
	default:
		return false
	}
}

func runnerEventText(eventItem events.Event) string {
	switch eventItem.Type {
	case events.RunStarted:
		return "run started"
	case events.RunFinished:
		return fmt.Sprintf("run finished: %s", eventItem.Status)
	case events.StepStarted:
		return fmt.Sprintf("start %s", eventItem.StepID)
	case events.StepFinished:
		message := strings.TrimSpace(eventItem.Message)
		if message != "" {
			return fmt.Sprintf("%s %s (%s)", stepTerminalVerb(eventItem.Status), eventItem.StepID, message)
		}
		return fmt.Sprintf("%s %s", stepTerminalVerb(eventItem.Status), eventItem.StepID)
	case events.StepSkipped:
		return fmt.Sprintf("skip %s (%s)", eventItem.StepID, eventItem.Message)
	case events.StepBlocked:
		return fmt.Sprintf("blocked %s (%s)", eventItem.StepID, eventItem.Message)
	case events.StepStale:
		return fmt.Sprintf("stale %s (%s)", eventItem.StepID, eventItem.Message)
	case events.GitHubStatusPosted:
		if eventItem.StepID == "" {
			return fmt.Sprintf("github posted %s", eventItem.Message)
		}
		return fmt.Sprintf("github posted %s [%s]", eventItem.Message, eventItem.StepID)
	case events.GitHubStatusFailed:
		if eventItem.StepID == "" {
			return fmt.Sprintf("github failed %s", eventItem.Message)
		}
		return fmt.Sprintf("github failed %s [%s]", eventItem.Message, eventItem.StepID)
	default:
		return string(eventItem.Type)
	}
}

func stepTerminalVerb(status string) string {
	switch status {
	case string(engine.StepStateSuccess):
		return "ok"
	case string(engine.StepStateFailure):
		return "fail"
	default:
		return status
	}
}

func writeJSON(writer io.Writer, value any) error {
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

type styles struct {
	enabled bool
}

func newStyles(writer io.Writer) styles {
	return styles{enabled: isTerminalWriter(writer)}
}

func (s styles) status(text string, state string) string {
	switch state {
	case string(engine.StepStateSuccess):
		return s.wrap("32", text)
	case string(engine.StepStateFailure):
		return s.wrap("31", text)
	case string(engine.StepStateBlocked), string(engine.StepStateSkipped), string(engine.StepStateStale):
		return s.wrap("33", text)
	case string(engine.StepStateRunning), string(engine.StepStatePending):
		return s.wrap("36", text)
	case "error":
		return s.wrap("31", text)
	default:
		return text
	}
}

func (s styles) runnerEvent(eventItem events.Event) string {
	line := eventItem.Time.Format(time.RFC3339) + " " + runnerEventText(eventItem)
	switch eventItem.Type {
	case events.StepFinished:
		return s.status(line, eventItem.Status)
	case events.StepSkipped, events.StepBlocked, events.StepStale:
		return s.status(line, eventItem.Status)
	case events.RunFinished:
		return s.status(line, eventItem.Status)
	default:
		return line
	}
}

func (s styles) progressLine(line string) string {
	trimmed := strings.TrimSuffix(line, "\n")
	switch {
	case strings.HasPrefix(trimmed, "ok "):
		return s.wrap("32", line)
	case strings.HasPrefix(trimmed, "fail "):
		return s.wrap("31", line)
	case strings.HasPrefix(trimmed, "blocked ") || strings.HasPrefix(trimmed, "skip ") || strings.HasPrefix(trimmed, "stale "):
		return s.wrap("33", line)
	case strings.HasPrefix(trimmed, "start ") || strings.HasPrefix(trimmed, "log "):
		return s.wrap("36", line)
	case strings.HasPrefix(trimmed, "run ") && strings.Contains(trimmed, "finished:"):
		parts := strings.Split(trimmed, "finished:")
		state := strings.TrimSpace(parts[len(parts)-1])
		return s.status(line, state)
	case strings.HasPrefix(trimmed, "run "):
		return s.wrap("36", line)
	default:
		return line
	}
}

func (s styles) wrap(code string, text string) string {
	if !s.enabled {
		return text
	}
	return "\x1b[" + code + "m" + text + "\x1b[0m"
}

func isTerminalWriter(writer io.Writer) bool {
	file, ok := writer.(*os.File)
	if !ok {
		return false
	}
	if term := os.Getenv("TERM"); term == "" || term == "dumb" {
		return false
	}
	info, err := file.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

type progressWriter struct {
	writer io.Writer
	style  styles
	buffer []byte
}

func newProgressWriter(writer io.Writer, style styles) io.Writer {
	if !style.enabled {
		return writer
	}
	return &progressWriter{writer: writer, style: style}
}

func (w *progressWriter) Write(payload []byte) (int, error) {
	w.buffer = append(w.buffer, payload...)
	for {
		index := bytes.IndexByte(w.buffer, '\n')
		if index < 0 {
			break
		}
		line := string(w.buffer[:index+1])
		if _, err := io.WriteString(w.writer, w.style.progressLine(line)); err != nil {
			return 0, err
		}
		w.buffer = w.buffer[index+1:]
	}
	return len(payload), nil
}

func (c *cli) printTopHelp() {
	_, _ = io.WriteString(c.stdout, `local-ci runs repo-owned verification steps and stores each run under .local-ci/runs/<run-id>/.

A run id is the timestamped directory name for one local CI session, for example:
  20260627T150405Z-deadbeef

Artifacts live inside the current git repo:
  .local-ci/runs/<run-id>/

Main commands:
  local-ci run                 Start a new run and stream progress.
  local-ci resume <run-id>     Resume a prior run with the same immutable run id.
  local-ci runs                List recent runs from disk, newest first.
  local-ci show <run-id>       Snapshot a finished or still-running run from persisted artifacts.
  local-ci logs <run-id>       Read runner, planner, or step logs from disk.
  local-ci publish <run-id>    Post a completed run to the current clean HEAD when the snapshot still matches.
  local-ci version             Print the installed version.
  local-ci manual              Print the built-in long-form manual.

Debugging flow:
  local-ci run
  local-ci runs
  local-ci show <run-id>
  local-ci logs <run-id>
  local-ci logs <run-id> --step <step-id>
  local-ci publish <run-id>

Snapshot trust model:
  show <run-id> surfaces head_tree_hash, worktree_tree_hash, dirty_worktree,
  and dirty_files so you can see exactly what code snapshot produced the run.

Log types:
  runner  Derived orchestration view from events.jsonl.
  planner Raw planner stderr/debug log from planner.log.
  step    Raw subprocess logs such as combined.log, stdout.log, stderr.log, output.env.

Use 'local-ci help <command>' or 'local-ci <command> --help' for command details.
Use 'local-ci manual' or 'local-ci help all' for the full built-in guide.
Use 'local-ci version' or 'local-ci --version' to print the installed version.
`)
}

func (c *cli) printVersionHelp() {
	_, _ = io.WriteString(c.stdout, `Usage:
  local-ci version
  local-ci --version

Print the installed local-ci version.

Notes:
  - Release builds print the tag version, for example v0.1.0.
  - Development builds print dev.
`)
}

func (c *cli) printRunHelp() {
	_, _ = io.WriteString(c.stdout, `Usage:
  local-ci run [--config <path>] [--no-github]

Start a new local CI run, stream progress live, and persist artifacts under .local-ci/runs/<run-id>/.

Flags:
  --config <path>   Config file path. Default: .local-ci.toml
  --no-github       Disable GitHub status posting for this execution

Examples:
  local-ci run
  local-ci run --config .local-ci.toml
  local-ci run --no-github

Notes:
  - Child stdout/stderr stream live.
  - Runner progress lines are separate from persisted step logs.
  - On step failure the CLI prints the exact combined log path immediately.
  - Use --no-github to keep the run local-only, then local-ci publish <run-id> later if needed.
`)
}

func (c *cli) printResumeHelp() {
	_, _ = io.WriteString(c.stdout, `Usage:
  local-ci resume <run-id> [--config <path>] [--no-github]

Resume a prior run using the same immutable run id.

Flags:
  --config <path>   Config file path. Default: .local-ci.toml
  --no-github       Disable GitHub status posting for this execution

Examples:
  local-ci resume 20260627T150405Z-deadbeef
  local-ci resume 20260627T150405Z-deadbeef --config .local-ci.toml
  local-ci resume 20260627T150405Z-deadbeef --no-github

Notes:
  - Resume reuses prior successful steps only when repo identity, HEAD SHA, config hash, and plan hash still match.
  - Resume fails closed when the stored run identity no longer matches the current checkout.
  - Use --no-github to keep the resumed execution local-only.
`)
}

func (c *cli) printRunsHelp() {
	_, _ = io.WriteString(c.stdout, `Usage:
  local-ci runs [--json]

List recent runs from .local-ci/runs/, newest first.

Flags:
  --json   Emit the same run list as JSON.

Examples:
  local-ci runs
  local-ci runs --json

Notes:
  - Active runs appear without a finished time.
  - Active runs show the stored runner PID when known.
  - Status is derived from persisted artifacts only; this command does not attach to a live process.
`)
}

func (c *cli) printShowHelp() {
	_, _ = io.WriteString(c.stdout, `Usage:
  local-ci show <run-id> [--json]

Show a snapshot of one run from persisted artifacts.

Flags:
  --json   Emit the same snapshot as JSON.

Examples:
  local-ci show 20260627T150405Z-deadbeef
  local-ci show 20260627T150405Z-deadbeef --json

Notes:
  - Works for both active and finished runs.
  - Reads meta.json, summary.json, events.jsonl, and per-step status.json from disk only.
  - Shows the stored runner PID for active runs when known.
  - Shows the stored tree snapshot and dirty-file manifest for the run.
  - Does not attach to the running process.
  - This is the main snapshot/debug entrypoint for humans and LLMs.
`)
}

func (c *cli) printPublishHelp() {
	_, _ = io.WriteString(c.stdout, `Usage:
  local-ci publish <run-id>

Publish a completed run to the current clean HEAD commit when the current HEAD tree
matches the exact worktree snapshot that originally produced the run.

The current config and resolved plan must also still match the stored run.

Examples:
  local-ci publish 20260627T150405Z-deadbeef

Notes:
  - This is for dirty-worktree runs or --no-github runs that intentionally skipped GitHub posting.
  - The current worktree must be clean.
  - The current HEAD tree must exactly match the stored run snapshot.
  - The current config and resolved plan must still match the stored run.
  - A run that already posted during execution is not publishable again.
  - If the code, config, or plan changed after the run, publish is refused instead of guessing.
`)
}

func (c *cli) printLogsHelp() {
	_, _ = io.WriteString(c.stdout, `Usage:
  local-ci logs <run-id> [--runner | --planner | --step <step-id>] [--stdout | --stderr | --combined | --output-env] [--json]

Read persisted logs for one run.

Defaults:
  - No selector: runner/orchestration view derived from events.jsonl.
  - --step without a file selector: combined.log.

Selectors:
  --runner       Show the runner/orchestration log derived from events.jsonl.
  --planner      Show raw planner.log.
  --step <id>    Show logs for one step.
  --stdout       With --step, show stdout.log.
  --stderr       With --step, show stderr.log.
  --combined     With --step, show combined.log.
  --output-env   With --step, show output.env.
  --json         Emit the selected log source as JSON.

Examples:
  local-ci logs 20260627T150405Z-deadbeef
  local-ci logs 20260627T150405Z-deadbeef --runner
  local-ci logs 20260627T150405Z-deadbeef --planner
  local-ci logs 20260627T150405Z-deadbeef --step checks-fast
  local-ci logs 20260627T150405Z-deadbeef --step checks-fast --stderr
  local-ci logs 20260627T150405Z-deadbeef --step checks-fast --output-env

Notes:
  - Runner logs and subprocess logs answer different questions; runner is the default on purpose.
  - Safe for active runs: files are read from disk only.
  - Conflicting selectors are rejected instead of guessed.
`)
}

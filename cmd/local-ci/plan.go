package main

import (
	"context"
	"fmt"
	"io"

	"github.com/DiversioTeam/local-ci-runner/internal/config"
	"github.com/DiversioTeam/local-ci-runner/internal/engine"
	"github.com/DiversioTeam/local-ci-runner/internal/events"
	"github.com/DiversioTeam/local-ci-runner/internal/persistence"
)

type planJSON struct {
	RepoRoot         string              `json:"repo_root"`
	RepoSlug         string              `json:"repo_slug"`
	HeadSHA          string              `json:"head_sha"`
	WorktreeTreeHash string              `json:"worktree_tree_hash"`
	DirtyWorktree    bool                `json:"dirty_worktree"`
	ConfigPath       string              `json:"config_path"`
	ConfigHash       string              `json:"config_hash"`
	PlanHash         string              `json:"plan_hash"`
	MaxParallel      int                 `json:"max_parallel"`
	Plan             config.ResolvedPlan `json:"plan"`
}

func (c *cli) planCommand(commandContext context.Context, args []string) error {
	opts, _, err := parseExecutionArgs(args, false)
	if err != nil {
		return err
	}
	if opts.noGitHub || opts.fromStep != "" {
		return fmt.Errorf("plan accepts --config, --max-parallel, and --json only; it never posts statuses or executes steps")
	}
	inputs, err := loadPlanInputs(commandContext, c.cwd, opts.configPath)
	if err != nil {
		return err
	}
	// Planner diagnostics must never become part of machine-readable stdout.
	if inputs.plannerLog != "" {
		_, _ = io.WriteString(c.stderr, inputs.plannerLog)
	}
	payload := planJSON{
		RepoRoot:         inputs.repository.Root,
		RepoSlug:         inputs.repository.RepoSlug,
		HeadSHA:          inputs.repository.HeadSHA,
		WorktreeTreeHash: inputs.repository.WorktreeTreeHash,
		DirtyWorktree:    inputs.repository.DirtyWorktree,
		ConfigPath:       inputs.identity.ConfigPath,
		ConfigHash:       inputs.identity.ConfigHash,
		PlanHash:         inputs.identity.PlanHash,
		MaxParallel:      getMaxParallel(inputs.configuration, opts),
		Plan:             inputs.plan,
	}
	if opts.json {
		return writeJSON(c.stdout, payload)
	}
	_, _ = fmt.Fprintf(c.stdout, "repo: %s @ %s\nconfig: %s\nconfig_hash: %s\nplan_hash: %s\nmax_parallel: %d\n",
		payload.RepoSlug, payload.HeadSHA, payload.ConfigPath, payload.ConfigHash, payload.PlanHash, payload.MaxParallel)
	for _, step := range inputs.plan.Steps {
		_, _ = fmt.Fprintf(c.stdout, "- %s: command=%q needs=%v if=%q timeout=%q context=%q\n",
			step.ID, step.Command, step.Needs, step.If, step.Timeout, step.GitHubContext)
	}
	_, _ = fmt.Fprintln(c.stdout, "No steps executed, run artifacts created, or statuses posted. Repo-owned planner code may have side effects.")
	return nil
}

func getMaxParallel(cfg config.File, opts executionCLIOptions) int {
	if opts.maxParallel > 0 {
		return opts.maxParallel
	}
	return max(1, cfg.MaxParallel)
}

func buildShowJSON(run engine.RunRecord, store persistence.Store, items []events.Event, eventError error) showJSON {
	payload := showJSON{
		RunID:          run.RunID,
		RunDir:         run.RunDir,
		Status:         displayRunStatus(run.Summary.Status, run.StepStatuses),
		RunnerAlive:    getRunnerAlive(run.Meta),
		Meta:           run.Meta,
		Summary:        run.Summary,
		Steps:          run.StepStatuses,
		RunnerLogPath:  store.RunFile(run.RunID, persistence.EventsFile),
		PlannerLogPath: store.RunFile(run.RunID, persistence.PlannerLogFile),
	}
	if len(items) > 0 {
		payload.LatestEvent = &items[len(items)-1]
	}
	if eventError != nil {
		payload.LatestEventError = eventError.Error()
	}
	return payload
}

func (c *cli) printPlanHelp() {
	c.printVersion()
	_, _ = io.WriteString(c.stdout, `Usage:
  local-ci plan [--config <path>] [--max-parallel <count>] [--json]

Load config, execute the repo-owned planner if configured, and explain the complete plan.

Flags:
  --config <path>         Config path. Default: .local-ci.toml
  --max-parallel <count>  Positive worker limit. Default: config max_parallel, otherwise 1
  --json                  Emit one JSON object with snapshot identity, hashes, max_parallel, and plan

Examples:
  local-ci plan
  local-ci plan --json
  local-ci plan --max-parallel 1 --json

Safety:
  - No verification steps run, run directories created, or GitHub statuses posted.
  - The planner executes repo-owned code and may write files or contact services.
    Obtain authorization before running plan; it is not guaranteed side-effect-free.
  - This command does not prove that checks passed or that GitHub statuses are current.
  - Planner diagnostics go to stderr; JSON alone goes to stdout.
  - Planning refuses changes to repository identity, HEAD, worktree snapshot, or config.
    Generated files must use ignored or runner-artifact paths.
  - Inspect dependencies before opting into parallelism; independent steps may share resources.
`)
}

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/DiversioTeam/local-ci-runner/internal/agentsession"
	"github.com/DiversioTeam/local-ci-runner/internal/config"
	"github.com/DiversioTeam/local-ci-runner/internal/gitrepo"
)

var errAgentSessionFailed = errors.New("agent-session check failed")

// agentSessionStep is the built-in step `agent_session = true` adds. It runs through PATH like any other step command,
// so the plan (and its hash) does not depend on where this binary is installed.
func agentSessionStep() config.Step {
	return config.Step{ID: agentsession.StepID, Name: agentsession.StepName, Command: []string{"local-ci", agentsession.Subcommand},
		GitHubContext: agentsession.GitHubContext}
}

// openPullRequest asks gh for the branch's open PR. No gh, no PR, or a gh error all mean "no PR known", and the check
// then relies on the local record.
func openPullRequest(ctx context.Context, repoSlug string, branch string) (*agentsession.PullRequest, string) {
	if _, err := exec.LookPath("gh"); err != nil {
		return nil, "gh not found; checked the local record only"
	}
	lookup, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	output, err := exec.CommandContext(lookup, "gh", "pr", "view", branch, "-R", repoSlug, "--json", "number,state,body").Output()
	if err != nil {
		return nil, ""
	}
	var view struct {
		Number int    `json:"number"`
		State  string `json:"state"`
		Body   string `json:"body"`
	}
	if json.Unmarshal(output, &view) != nil || view.State != "OPEN" || view.Number == 0 {
		return nil, ""
	}
	return &agentsession.PullRequest{Number: view.Number, Body: view.Body}, ""
}

func (c *cli) agentSessionCheckCommand(ctx context.Context) error {
	repository, err := gitrepo.Discover(ctx, c.cwd)
	if err != nil {
		return err
	}
	branchOutput, _ := exec.CommandContext(ctx, "git", "-C", repository.Root, "branch", "--show-current").Output()
	branch := strings.TrimSpace(string(branchOutput))
	home, _ := os.UserHomeDir()
	published, err := agentsession.LatestPublished(agentsession.StorePath(home), repository.RepoSlug, branch)
	if err != nil {
		return fmt.Errorf("read the agent-session record: %w", err)
	}
	var pr *agentsession.PullRequest
	note := ""
	if branch != "" {
		pr, note = openPullRequest(ctx, repository.RepoSlug, branch)
	}
	result := agentsession.Check(agentsession.Input{RepoRoot: repository.Root, RepoSlug: repository.RepoSlug, Branch: branch,
		HeadSHA: repository.HeadSHA, PR: pr, Published: published, CLI: agentsession.FindCLI(home)})
	fmt.Fprintln(c.stdout, result.Message)
	if note != "" {
		fmt.Fprintln(c.stdout, "note: "+note)
	}
	if !result.OK {
		return errAgentSessionFailed
	}
	return nil
}

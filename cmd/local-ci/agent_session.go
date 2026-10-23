package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/DiversioTeam/local-ci-runner/internal/agentsession"
	"github.com/DiversioTeam/local-ci-runner/internal/config"
	ghstatus "github.com/DiversioTeam/local-ci-runner/internal/github"
	"github.com/DiversioTeam/local-ci-runner/internal/gitrepo"
)

var errAgentSessionFailed = errors.New("agent-session check failed")

// agentSessionStep is the built-in step `agent_session = true` adds. It runs through PATH like any other step command,
// so the plan (and its hash) does not depend on where this binary is installed.
func agentSessionStep() config.Step {
	return config.Step{ID: agentsession.StepID, Name: agentsession.StepName, Command: []string{"local-ci", agentsession.Subcommand},
		GitHubContext: agentsession.GitHubContext}
}

// openPullRequest asks GitHub for the branch's open PR, with the same credentials and host as status posting. A
// confirmed absence returns nil; a failed lookup (no gh, auth, network, rate limit) returns an error, and the check then
// fails closed instead of trusting the local record.
func openPullRequest(ctx context.Context, repoSlug string, branch string) (*agentsession.PullRequest, error) {
	if _, err := exec.LookPath("gh"); err != nil {
		return nil, errors.New("gh is not installed")
	}
	owner, _, _ := strings.Cut(repoSlug, "/")
	lookup, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	path := fmt.Sprintf("repos/%s/pulls?state=open&head=%s", repoSlug, url.QueryEscape(owner+":"+branch))
	command := exec.CommandContext(lookup, "gh", "api", "--hostname", "github.com", path)
	command.Env = ghstatus.CLIEnv(os.Environ(), os.Getenv(ghstatus.TokenEnvVar))
	output, err := command.Output()
	if err != nil {
		return nil, errors.New("gh could not read the PR (check gh auth status or LOCAL_CI_GITHUB_TOKEN)")
	}
	var pulls []struct {
		Number int    `json:"number"`
		Body   string `json:"body"`
	}
	if err := json.Unmarshal(output, &pulls); err != nil {
		return nil, errors.New("GitHub returned an unreadable PR list")
	}
	if len(pulls) == 0 {
		return nil, nil
	}
	return &agentsession.PullRequest{Number: pulls[0].Number, Body: pulls[0].Body}, nil
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
	lookupError := ""
	if branch != "" {
		if pr, err = openPullRequest(ctx, repository.RepoSlug, branch); err != nil {
			lookupError = err.Error()
		}
	}
	result := agentsession.Check(agentsession.Input{RepoRoot: repository.Root, RepoSlug: repository.RepoSlug, Branch: branch,
		HeadSHA: repository.HeadSHA, PR: pr, PRLookupError: lookupError, Published: published, CLI: agentsession.FindCLI(home)})
	fmt.Fprintln(c.stdout, result.Message)
	if !result.OK {
		return errAgentSessionFailed
	}
	return nil
}

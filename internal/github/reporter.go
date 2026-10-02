package github

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

type Reporter interface {
	PostStatus(ctx context.Context, target Target, status Status) error
}

// CLIReporter posts commit statuses through the gh CLI found on PATH.
type CLIReporter struct {
	Token string
}

func (reporter CLIReporter) PostStatus(ctx context.Context, target Target, status Status) error {
	spec, err := reporter.commandSpec(target, status)
	if err != nil {
		return err
	}

	cmd := exec.CommandContext(ctx, spec.name, spec.args...)
	cmd.Env = spec.env
	// Bound orphaned output pipes after the report context is canceled.
	cmd.WaitDelay = time.Second
	output, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("run %s: %w", spec.name, context.Cause(ctx))
		}
		message := strings.TrimSpace(string(output))
		if message == "" {
			return fmt.Errorf("run %s: %w", spec.name, err)
		}
		return fmt.Errorf("run %s: %s: %w", spec.name, message, err)
	}

	return nil
}

type commandSpec struct {
	name string
	args []string
	env  []string
}

func (reporter CLIReporter) commandSpec(target Target, status Status) (commandSpec, error) {
	if err := validateTarget(target); err != nil {
		return commandSpec{}, err
	}
	if err := validateStatus(status); err != nil {
		return commandSpec{}, err
	}

	args := []string{
		"api",
		fmt.Sprintf("repos/%s/statuses/%s", target.Repo, target.SHA),
		"-X", "POST",
		"-f", "state=" + string(status.State),
		"-f", "context=" + status.Context,
	}
	if status.Description != "" {
		args = append(args, "-f", "description="+status.Description)
	}

	// Repository discovery accepts github.com only; inherited GH_HOST must not retarget receipts.
	args = append(args, "--hostname", "github.com")
	return commandSpec{
		name: "gh",
		args: args,
		env:  cliEnv(os.Environ(), reporter.Token),
	}, nil
}

func validateTarget(target Target) error {
	if strings.TrimSpace(target.Repo) == "" {
		return fmt.Errorf("GitHub repo is required")
	}
	if strings.TrimSpace(target.SHA) == "" {
		return fmt.Errorf("GitHub SHA is required")
	}
	return nil
}

func validateStatus(status Status) error {
	if strings.TrimSpace(status.Context) == "" {
		return fmt.Errorf("GitHub status context is required")
	}
	switch status.State {
	case StatePending, StateSuccess, StateFailure, StateError:
		return nil
	default:
		return fmt.Errorf("GitHub status state %q is not supported", status.State)
	}
}

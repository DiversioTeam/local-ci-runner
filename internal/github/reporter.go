package github

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

type Reporter interface {
	PostStatus(ctx context.Context, target Target, status Status) error
}

type CLIReporter struct {
	Command string
	Token   string
	Env     []string
}

func (reporter CLIReporter) PostStatus(ctx context.Context, target Target, status Status) error {
	spec, err := reporter.commandSpec(target, status)
	if err != nil {
		return err
	}

	cmd := exec.CommandContext(resolveContext(ctx), spec.name, spec.args...)
	cmd.Env = spec.env
	output, err := cmd.CombinedOutput()
	if err != nil {
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

	commandName := reporter.Command
	if commandName == "" {
		commandName = "gh"
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
	if status.TargetURL != "" {
		args = append(args, "-f", "target_url="+status.TargetURL)
	}

	baseEnv := reporter.Env
	if baseEnv == nil {
		baseEnv = os.Environ()
	}

	return commandSpec{
		name: commandName,
		args: args,
		env:  CLIEnv(baseEnv, reporter.Token),
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

func resolveContext(ctx context.Context) context.Context {
	if ctx != nil {
		return ctx
	}
	return context.Background()
}

func validateStatus(status Status) error {
	if strings.TrimSpace(status.Context) == "" {
		return fmt.Errorf("GitHub status context is required")
	}
	switch status.State {
	case StatePending, StateSuccess, StateFailure:
		return nil
	default:
		return fmt.Errorf("GitHub status state %q is not supported", status.State)
	}
}

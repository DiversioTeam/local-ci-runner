package planner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/DiversioTeam/local-ci-runner/internal/config"
)

type Result struct {
	Plan config.ResolvedPlan
	Log  string
}

func Execute(
	ctx context.Context,
	repoRoot string,
	repoSlug string,
	headSHA string,
	configPath string,
	plannerConfig config.Planner,
) (Result, error) {
	ctx = resolveContext(ctx)

	command, err := resolveCommand(plannerConfig.Command)
	if err != nil {
		return Result{}, err
	}
	workingDir := resolveDir(repoRoot, plannerConfig.Dir)

	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	cmd.Dir = workingDir
	cmd.Env = plannerEnv(repoRoot, repoSlug, headSHA, configPath, plannerConfig.Env)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = strings.TrimSpace(stdout.String())
		}
		if message == "" {
			return Result{}, fmt.Errorf("run planner: %w", err)
		}
		return Result{}, fmt.Errorf("run planner: %s: %w", message, err)
	}

	var plan config.ResolvedPlan
	if err := json.Unmarshal(stdout.Bytes(), &plan); err != nil {
		return Result{}, fmt.Errorf("decode planner output: %w", err)
	}
	plan.ApplyDefaults()
	if err := plan.Validate(); err != nil {
		return Result{}, fmt.Errorf("validate planner output: %w", err)
	}

	return Result{Plan: plan, Log: stderr.String()}, nil
}

func resolveCommand(command []string) ([]string, error) {
	if len(command) == 0 {
		return nil, fmt.Errorf("planner command must not be empty")
	}
	if strings.TrimSpace(command[0]) == "" {
		return nil, fmt.Errorf("planner command must start with a non-empty executable")
	}
	return append([]string(nil), command...), nil
}

func resolveDir(repoRoot string, dir string) string {
	if dir == "" {
		dir = config.DefaultWorkingDir
	}
	if filepath.IsAbs(dir) {
		return dir
	}
	return filepath.Join(repoRoot, dir)
}

func plannerEnv(repoRoot string, repoSlug string, headSHA string, configPath string, extra map[string]string) []string {
	env := make(map[string]string)
	for _, item := range os.Environ() {
		key, value, ok := strings.Cut(item, "=")
		if !ok {
			continue
		}
		env[key] = value
	}
	for key, value := range extra {
		env[key] = value
	}

	env["LOCAL_CI_REPO_ROOT"] = repoRoot
	env["LOCAL_CI_CONFIG"] = configPath
	env["LOCAL_CI_GITHUB_REPO"] = repoSlug
	env["LOCAL_CI_GITHUB_SHA"] = headSHA

	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+env[key])
	}
	return result
}

func resolveContext(ctx context.Context) context.Context {
	if ctx != nil {
		return ctx
	}
	return context.Background()
}

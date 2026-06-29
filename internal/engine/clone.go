package engine

import (
	"github.com/DiversioTeam/local-ci-runner/internal/config"
	"github.com/DiversioTeam/local-ci-runner/internal/persistence"
)

func cloneSteps(src []config.Step) []config.Step {
	if src == nil {
		return nil
	}

	dst := make([]config.Step, len(src))
	for index, step := range src {
		dst[index] = cloneStep(step)
	}
	return dst
}

func cloneStep(step config.Step) config.Step {
	return config.Step{
		ID:            step.ID,
		Name:          step.Name,
		Command:       cloneStrings(step.Command),
		Dir:           step.Dir,
		Needs:         cloneStrings(step.Needs),
		If:            step.If,
		GitHubContext: step.GitHubContext,
		Env:           cloneStringMap(step.Env),
	}
}

func cloneStrings(src []string) []string {
	return append([]string(nil), src...)
}

func cloneStringMap(src map[string]string) map[string]string {
	if src == nil {
		return nil
	}

	dst := make(map[string]string, len(src))
	for key, value := range src {
		dst[key] = value
	}
	return dst
}

func cloneWorktreeFiles(src []persistence.WorktreeFile) []persistence.WorktreeFile {
	if src == nil {
		return nil
	}
	dst := make([]persistence.WorktreeFile, len(src))
	copy(dst, src)
	return dst
}

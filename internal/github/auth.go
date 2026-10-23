package github

import "strings"

const (
	TokenEnvVar       = "LOCAL_CI_GITHUB_TOKEN"
	ghTokenEnvVar     = "GH_TOKEN"
	githubTokenEnvVar = "GITHUB_TOKEN"
)

// CLIEnv is cliEnv for other gh callers in this binary (the agent-session PR lookup).
func CLIEnv(base []string, token string) []string { return cliEnv(base, token) }

// cliEnv drops inherited generic GitHub tokens, so only LOCAL_CI_GITHUB_TOKEN or gh auth can post.
func cliEnv(base []string, token string) []string {
	env := make([]string, 0, len(base)+2)
	for _, item := range base {
		key, _, ok := strings.Cut(item, "=")
		if !ok {
			env = append(env, item)
			continue
		}
		if key == ghTokenEnvVar || key == githubTokenEnvVar {
			continue
		}
		env = append(env, item)
	}
	if token == "" {
		return env
	}

	env = append(env,
		ghTokenEnvVar+"="+token,
		githubTokenEnvVar+"="+token,
	)
	return env
}

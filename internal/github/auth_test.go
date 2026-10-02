package github

import (
	"slices"
	"testing"
)

func TestCLIEnv(t *testing.T) {
	t.Parallel()

	base := []string{"A=1", "GH_TOKEN=old", "GITHUB_TOKEN=older", "B=2"}
	for _, test := range []struct {
		name  string
		token string
		want  []string
	}{
		// Without a runner token, gh falls back to its own auth, never an ambient token.
		{name: "no runner token", want: []string{"A=1", "B=2"}},
		{name: "runner token", token: "runner-token", want: []string{"A=1", "B=2", "GH_TOKEN=runner-token", "GITHUB_TOKEN=runner-token"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := cliEnv(base, test.token); !slices.Equal(got, test.want) {
				t.Fatalf("env = %v, want %v", got, test.want)
			}
		})
	}
	// Operators configure this name; auth variables this tool reads must carry the LOCAL_CI_ prefix.
	if TokenEnvVar != "LOCAL_CI_GITHUB_TOKEN" {
		t.Fatalf("TokenEnvVar = %q", TokenEnvVar)
	}
}

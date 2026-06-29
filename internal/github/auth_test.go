package github

import "testing"

func TestCLIEnvWithoutTokenStripsGenericGitHubTokens(t *testing.T) {
	t.Parallel()

	base := []string{"A=1", "GH_TOKEN=old", "GITHUB_TOKEN=older", "B=2"}
	got := CLIEnv(base, "")

	want := []string{"A=1", "B=2"}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("env[%d] = %q, want %q", index, got[index], want[index])
		}
	}
}

func TestCLIEnvInjectsRunnerToken(t *testing.T) {
	t.Parallel()

	base := []string{"A=1", "GH_TOKEN=old", "GITHUB_TOKEN=older", "B=2"}
	got := CLIEnv(base, "runner-token")

	want := []string{"A=1", "B=2", "GH_TOKEN=runner-token", "GITHUB_TOKEN=runner-token"}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("env[%d] = %q, want %q", index, got[index], want[index])
		}
	}
}

package github

import (
	"testing"
)

func TestCLIReporterCommandSpec(t *testing.T) {
	t.Parallel()

	reporter := CLIReporter{
		Token: "runner-token",
		Env:   []string{"A=1", "GH_TOKEN=old", "B=2"},
	}

	spec, err := reporter.commandSpec(
		Target{Repo: "owner/repo", SHA: "abc123"},
		Status{Context: "local/verify", State: StatePending, Description: "running"},
	)
	if err != nil {
		t.Fatalf("commandSpec() error = %v", err)
	}

	if got, want := spec.name, "gh"; got != want {
		t.Fatalf("name = %q, want %q", got, want)
	}
	wantArgs := []string{
		"api",
		"repos/owner/repo/statuses/abc123",
		"-X", "POST",
		"-f", "state=pending",
		"-f", "context=local/verify",
		"-f", "description=running",
	}
	assertStringsEqual(t, spec.args, wantArgs)
	wantEnv := []string{"A=1", "B=2", "GH_TOKEN=runner-token", "GITHUB_TOKEN=runner-token"}
	assertStringsEqual(t, spec.env, wantEnv)
}

func TestCLIReporterRejectsInvalidInput(t *testing.T) {
	t.Parallel()

	reporter := CLIReporter{}
	if _, err := reporter.commandSpec(Target{}, Status{Context: "local/verify", State: StatePending}); err == nil {
		t.Fatal("expected target error")
	}
	if _, err := reporter.commandSpec(Target{Repo: "owner/repo", SHA: "abc123"}, Status{}); err == nil {
		t.Fatal("expected status error")
	}
}

func assertStringsEqual(t *testing.T, got []string, want []string) {
	t.Helper()

	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("item[%d] = %q, want %q", index, got[index], want[index])
		}
	}
}

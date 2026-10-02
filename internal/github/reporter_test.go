package github

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestCLIReporterPostsThroughGH runs PostStatus against a fake gh on PATH and checks what
// gh actually receives. It changes PATH, so it cannot run in parallel.
func TestCLIReporterPostsThroughGH(t *testing.T) {
	binDir := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "gh.log")
	script := "#!/bin/sh\nfor arg in \"$@\"; do printf 'arg %s\\n' \"$arg\"; done >> \"$GH_LOG\"\n" +
		"printf 'env GH_TOKEN=%s GITHUB_TOKEN=%s\\n' \"$GH_TOKEN\" \"$GITHUB_TOKEN\" >> \"$GH_LOG\"\n"
	if err := os.WriteFile(filepath.Join(binDir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)
	t.Setenv("GH_LOG", logPath)
	t.Setenv("GH_TOKEN", "ambient")
	t.Setenv("GH_HOST", "github.example.com")

	for _, status := range []Status{
		{Context: "local/verify", State: StatePending, Description: "running"},
		// Interrupted runs post error.
		{Context: "local/verify", State: StateError, Description: "interrupted"},
	} {
		if err := os.Remove(logPath); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		err := CLIReporter{Token: "runner-token"}.PostStatus(t.Context(), Target{Repo: "owner/repo", SHA: "abc123"}, status)
		if err != nil {
			t.Fatalf("PostStatus(%s) error = %v", status.State, err)
		}
		payload, err := os.ReadFile(logPath)
		if err != nil {
			t.Fatal(err)
		}
		want := []string{
			"arg api", "arg repos/owner/repo/statuses/abc123", "arg -X", "arg POST",
			"arg -f", "arg state=" + string(status.State), "arg -f", "arg context=local/verify",
			"arg -f", "arg description=" + status.Description,
			// Discovery accepts github.com only, so an inherited GH_HOST must not retarget posts.
			"arg --hostname", "arg github.com",
			"env GH_TOKEN=runner-token GITHUB_TOKEN=runner-token",
		}
		if got := strings.Split(strings.TrimSpace(string(payload)), "\n"); !slices.Equal(got, want) {
			t.Fatalf("gh received:\n%s", payload)
		}
	}
}

func TestCLIReporterReturnsCancellationCause(t *testing.T) {
	t.Parallel()

	cancellationCause := errors.New("stop reporting")
	reportContext, cancelReport := context.WithCancelCause(t.Context())
	cancelReport(cancellationCause)

	err := CLIReporter{}.PostStatus(reportContext, Target{Repo: "owner/repo", SHA: "abc123"}, Status{Context: "local/verify", State: StatePending})
	if !errors.Is(err, cancellationCause) {
		t.Fatalf("PostStatus() error = %v, want %v", err, cancellationCause)
	}
}

// TestMain shadows the real gh with one that always fails, so a broken guard can never post.
func TestMain(m *testing.M) {
	os.Exit(runWithFakeGH(m))
}

func runWithFakeGH(m *testing.M) int {
	fakeBin, err := os.MkdirTemp("", "local-ci-fake-gh-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(fakeBin)
	if err := os.WriteFile(filepath.Join(fakeBin, "gh"), []byte("#!/bin/sh\necho 'fake gh: tests must not contact GitHub' >&2\nexit 1\n"), 0o755); err != nil {
		panic(err)
	}
	os.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return m.Run()
}

func TestCLIReporterRejectsInvalidInput(t *testing.T) {
	t.Parallel()

	valid := Status{Context: "local/verify", State: StatePending}
	for _, test := range []struct {
		name      string
		target    Target
		status    Status
		wantError string
	}{
		{name: "missing repo", target: Target{SHA: "abc123"}, status: valid, wantError: "repo is required"},
		{name: "missing SHA", target: Target{Repo: "owner/repo"}, status: valid, wantError: "SHA is required"},
		{name: "missing context", target: Target{Repo: "owner/repo", SHA: "abc123"}, status: Status{State: StatePending}, wantError: "context is required"},
		{name: "unsupported state", target: Target{Repo: "owner/repo", SHA: "abc123"}, status: Status{Context: "local/verify", State: "bogus"}, wantError: `state "bogus" is not supported`},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Validation happens before gh is started, so check it without executing anything.
			if _, err := (CLIReporter{}).commandSpec(test.target, test.status); err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("error = %v, want %q", err, test.wantError)
			}
		})
	}
}

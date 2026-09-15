package update

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestIsNewerVersion(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		current string
		latest  string
		want    bool
	}{
		{name: "simple newer", current: "v0.1.0", latest: "v0.2.0", want: true},
		{name: "same", current: "v0.1.0", latest: "v0.1.0", want: false},
		{name: "older latest", current: "v0.2.0", latest: "v0.1.0", want: false},
		{name: "more parts", current: "v1.2", latest: "v1.2.1", want: true},
		{name: "invalid current", current: "dev", latest: "v1.0.0", want: false},
		{name: "invalid latest", current: "v1.0.0", latest: "latest", want: false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := isNewerVersion(testCase.current, testCase.latest); got != testCase.want {
				t.Fatalf("isNewerVersion(%q, %q) = %t, want %t", testCase.current, testCase.latest, got, testCase.want)
			}
		})
	}
}

func TestCheckerNoticeUsesCache(t *testing.T) {
	t.Parallel()

	serverHits := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		serverHits++
		_, _ = writer.Write([]byte(`{"tag_name":"v0.2.0","html_url":"https://example.com/release"}`))
	}))
	defer server.Close()

	cachePath := filepath.Join(t.TempDir(), "update.json")
	now := time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
	checker := Checker{
		CurrentVersion:   "v0.1.0",
		LatestReleaseURL: server.URL,
		CachePath:        cachePath,
		CacheTTL:         time.Hour,
		HTTPClient:       server.Client(),
		Now:              func() time.Time { return now },
		ExecutablePath:   "/home/dev/.local/bin/local-ci",
	}

	message, err := checker.Notice(context.Background())
	if err != nil {
		t.Fatalf("Notice() error = %v", err)
	}
	if !strings.Contains(message, "update available: v0.1.0 -> v0.2.0") {
		t.Fatalf("message = %q", message)
	}
	if !strings.Contains(message, scriptUpgradeCommand) {
		t.Fatalf("message = %q, want the install-script upgrade command", message)
	}
	message, err = checker.Notice(context.Background())
	if err != nil {
		t.Fatalf("Notice() second call error = %v", err)
	}
	if message == "" {
		t.Fatal("expected cached update message")
	}
	if got, want := serverHits, 1; got != want {
		t.Fatalf("server hits = %d, want %d", got, want)
	}
}

func TestIsHomebrewPath(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		path string
		want bool
	}{
		{name: "macos arm cellar", path: "/opt/homebrew/Cellar/local-ci/0.2.0/bin/local-ci", want: true},
		{name: "macos intel cellar", path: "/usr/local/Cellar/local-ci/0.2.0/bin/local-ci", want: true},
		{name: "linuxbrew", path: "/home/linuxbrew/.linuxbrew/Cellar/local-ci/0.2.0/bin/local-ci", want: true},
		{name: "install script default", path: "/home/dev/.local/bin/local-ci", want: false},
		{name: "install script system wide", path: "/usr/local/bin/local-ci", want: false},
		{name: "go install", path: "/home/dev/go/bin/local-ci", want: false},
		{name: "cellar as substring only", path: "/home/dev/Cellars/bin/local-ci", want: false},
		{name: "empty", path: "", want: false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := isHomebrewPath(testCase.path); got != testCase.want {
				t.Fatalf("isHomebrewPath(%q) = %t, want %t", testCase.path, got, testCase.want)
			}
		})
	}
}

func TestCheckerUpgradeCommand(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name           string
		executablePath string
		want           string
	}{
		{
			name:           "homebrew install",
			executablePath: "/opt/homebrew/Cellar/local-ci/0.2.0/bin/local-ci",
			want:           brewUpgradeCommand,
		},
		{
			name:           "install script",
			executablePath: "/home/dev/.local/bin/local-ci",
			want:           scriptUpgradeCommand,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			checker := Checker{ExecutablePath: testCase.executablePath}
			if got := checker.upgradeCommand(); got != testCase.want {
				t.Fatalf("upgradeCommand() = %q, want %q", got, testCase.want)
			}
		})
	}
}

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
	}

	message, err := checker.Notice(context.Background())
	if err != nil {
		t.Fatalf("Notice() error = %v", err)
	}
	if !strings.Contains(message, "update available: v0.1.0 -> v0.2.0") {
		t.Fatalf("message = %q", message)
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

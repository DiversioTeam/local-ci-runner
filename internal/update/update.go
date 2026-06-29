package update

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	DefaultRepo     = "DiversioTeam/local-ci-runner"
	DefaultVersion  = "dev"
	DefaultCacheTTL = 12 * time.Hour
)

var Version = DefaultVersion

type CacheEntry struct {
	CurrentVersion string    `json:"current_version"`
	CheckedAt      time.Time `json:"checked_at"`
	LatestVersion  string    `json:"latest_version,omitempty"`
	LatestURL      string    `json:"latest_url,omitempty"`
}

type Checker struct {
	CurrentVersion   string
	Repo             string
	LatestReleaseURL string
	CachePath        string
	CacheTTL         time.Duration
	HTTPClient       *http.Client
	Now              func() time.Time
}

type releaseResponse struct {
	TagName string `json:"tag_name"`
	HTMLURL string `json:"html_url"`
}

func (checker Checker) Notice(ctx context.Context) (string, error) {
	currentVersion := checker.currentVersion()
	if currentVersion == DefaultVersion {
		return "", nil
	}

	entry, err := checker.loadOrRefresh(ctx)
	if err != nil {
		return "", err
	}
	if !isNewerVersion(currentVersion, entry.LatestVersion) {
		return "", nil
	}
	return fmt.Sprintf(
		"update available: %s -> %s; run: brew update && brew upgrade local-ci",
		currentVersion,
		entry.LatestVersion,
	), nil
}

func (checker Checker) currentVersion() string {
	if strings.TrimSpace(checker.CurrentVersion) != "" {
		return checker.CurrentVersion
	}
	return Version
}

func (checker Checker) repo() string {
	if strings.TrimSpace(checker.Repo) != "" {
		return checker.Repo
	}
	return DefaultRepo
}

func (checker Checker) cachePath() (string, error) {
	if strings.TrimSpace(checker.CachePath) != "" {
		return checker.CachePath, nil
	}
	cacheRoot, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("resolve user cache dir: %w", err)
	}
	return filepath.Join(cacheRoot, "local-ci-runner", "update-check.json"), nil
}

func (checker Checker) cacheTTL() time.Duration {
	if checker.CacheTTL > 0 {
		return checker.CacheTTL
	}
	return DefaultCacheTTL
}

func (checker Checker) httpClient() *http.Client {
	if checker.HTTPClient != nil {
		return checker.HTTPClient
	}
	return &http.Client{Timeout: 1200 * time.Millisecond}
}

func (checker Checker) now() time.Time {
	if checker.Now != nil {
		return checker.Now().UTC()
	}
	return time.Now().UTC()
}

func (checker Checker) latestReleaseURL() string {
	if strings.TrimSpace(checker.LatestReleaseURL) != "" {
		return checker.LatestReleaseURL
	}
	return fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", checker.repo())
}

func (checker Checker) loadOrRefresh(ctx context.Context) (CacheEntry, error) {
	cachePath, err := checker.cachePath()
	if err != nil {
		return CacheEntry{}, err
	}
	entry, ok, err := checker.loadFreshCache(cachePath)
	if err != nil {
		return CacheEntry{}, err
	}
	if ok {
		return entry, nil
	}
	entry, err = checker.fetchLatest(ctx)
	if err != nil {
		return CacheEntry{}, err
	}
	if err := checker.writeCache(cachePath, entry); err != nil {
		return CacheEntry{}, err
	}
	return entry, nil
}

func (checker Checker) loadFreshCache(path string) (CacheEntry, bool, error) {
	payload, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return CacheEntry{}, false, nil
		}
		return CacheEntry{}, false, fmt.Errorf("read %s: %w", path, err)
	}
	var entry CacheEntry
	if err := json.Unmarshal(payload, &entry); err != nil {
		return CacheEntry{}, false, fmt.Errorf("decode %s: %w", path, err)
	}
	if entry.CurrentVersion != checker.currentVersion() {
		return CacheEntry{}, false, nil
	}
	if checker.now().Sub(entry.CheckedAt) > checker.cacheTTL() {
		return CacheEntry{}, false, nil
	}
	return entry, true, nil
}

func (checker Checker) fetchLatest(ctx context.Context) (CacheEntry, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, checker.latestReleaseURL(), nil)
	if err != nil {
		return CacheEntry{}, fmt.Errorf("build update request: %w", err)
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "local-ci-runner/"+checker.currentVersion())

	response, err := checker.httpClient().Do(request)
	if err != nil {
		return CacheEntry{}, fmt.Errorf("fetch latest release: %w", err)
	}
	defer func() {
		_ = response.Body.Close()
	}()
	if response.StatusCode == http.StatusNotFound {
		return CacheEntry{CurrentVersion: checker.currentVersion(), CheckedAt: checker.now()}, nil
	}
	if response.StatusCode != http.StatusOK {
		payload, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
		return CacheEntry{}, fmt.Errorf("fetch latest release: %s: %s", response.Status, strings.TrimSpace(string(payload)))
	}

	var release releaseResponse
	if err := json.NewDecoder(response.Body).Decode(&release); err != nil {
		return CacheEntry{}, fmt.Errorf("decode latest release: %w", err)
	}
	return CacheEntry{
		CurrentVersion: checker.currentVersion(),
		CheckedAt:      checker.now(),
		LatestVersion:  strings.TrimSpace(release.TagName),
		LatestURL:      strings.TrimSpace(release.HTMLURL),
	}, nil
}

func (checker Checker) writeCache(path string, entry CacheEntry) error {
	payload, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshal update cache: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create cache dir: %w", err)
	}
	if err := os.WriteFile(path, append(payload, '\n'), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func isNewerVersion(current string, latest string) bool {
	currentParts, currentOK := parseVersionParts(current)
	latestParts, latestOK := parseVersionParts(latest)
	if !currentOK || !latestOK {
		return false
	}
	for index := 0; index < len(currentParts) || index < len(latestParts); index++ {
		currentPart := versionPartAt(currentParts, index)
		latestPart := versionPartAt(latestParts, index)
		if latestPart > currentPart {
			return true
		}
		if latestPart < currentPart {
			return false
		}
	}
	return false
}

func parseVersionParts(raw string) ([]int, bool) {
	trimmed := strings.TrimPrefix(strings.TrimSpace(raw), "v")
	if trimmed == "" {
		return nil, false
	}
	parts := strings.Split(trimmed, ".")
	result := make([]int, 0, len(parts))
	for _, part := range parts {
		if part == "" {
			return nil, false
		}
		value := 0
		for _, r := range part {
			if r < '0' || r > '9' {
				return nil, false
			}
			value = value*10 + int(r-'0')
		}
		result = append(result, value)
	}
	return result, true
}

func versionPartAt(parts []int, index int) int {
	if index >= len(parts) {
		return 0
	}
	return parts[index]
}

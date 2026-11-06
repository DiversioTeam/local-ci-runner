package resultcache

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/DiversioTeam/local-ci-runner/internal/persistence"
)

// Version is the entry format. Entries with another version are ignored.
const Version = 3

// Entry points at one finished, all-success run that executed in a snapshot
// holding TestedTreeHash.
type Entry struct {
	Version        int       `json:"version"`
	RepoSlug       string    `json:"repo_slug"`
	RepoRoot       string    `json:"repo_root"`
	RunID          string    `json:"run_id"`
	RunDir         string    `json:"run_dir"`
	HeadSHA        string    `json:"head_sha"`
	TestedTreeHash string    `json:"tested_tree_hash"`
	ConfigHash     string    `json:"config_hash"`
	ConfigLocation string    `json:"config_location"`
	SeedHash       string    `json:"seed_hash"`
	PlanHash       string    `json:"plan_hash"`
	FinishedAt     time.Time `json:"finished_at"`
}

// Key names the code and checks an entry covers.
type Key struct {
	RepoSlug       string
	TestedTreeHash string
	ConfigHash     string
	ConfigLocation string
	SeedHash       string
	PlanHash       string
}

var hexPattern = regexp.MustCompile(`^[0-9a-f]{40,64}$`)
var slugPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// GetConfigLocation returns the repo-relative config path, or empty for external configs.
func GetConfigLocation(repoRoot, configPath string) string {
	if !filepath.IsAbs(repoRoot) || !filepath.IsAbs(configPath) {
		return ""
	}
	location, err := filepath.Rel(repoRoot, configPath)
	if err != nil || !filepath.IsLocal(location) {
		return ""
	}
	return filepath.ToSlash(location)
}

// Root is the per-user index directory: <UserCacheDir>/local-ci-runner/results.
func Root() (string, error) {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("locate user cache directory: %w", err)
	}
	return filepath.Join(cacheDir, "local-ci-runner", "results"), nil
}

// Path is where the entry for key lives under root.
func Path(root string, key Key) (string, error) {
	if !slugPattern.MatchString(key.RepoSlug) || strings.Contains(key.RepoSlug, "..") {
		return "", fmt.Errorf("invalid repository slug %q", key.RepoSlug)
	}
	for name, value := range map[string]string{"tree": key.TestedTreeHash, "config hash": key.ConfigHash, "plan hash": key.PlanHash, "seed hash": key.SeedHash} {
		if !hexPattern.MatchString(value) {
			return "", fmt.Errorf("invalid %s %q", name, value)
		}
	}
	if !filepath.IsLocal(key.ConfigLocation) {
		return "", fmt.Errorf("invalid config location %q", key.ConfigLocation)
	}
	inputHash := sha256.Sum256([]byte(key.ConfigLocation + "\x00" + key.SeedHash))
	return filepath.Join(root, filepath.FromSlash(key.RepoSlug), key.TestedTreeHash, fmt.Sprintf("%s-%s-%x.json", key.ConfigHash, key.PlanHash, inputHash)), nil
}

// Record writes the entry atomically, replacing any earlier one for its key.
func Record(root string, entry Entry) error {
	entry.Version = Version
	path, err := Path(root, entry.key())
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create result index directory: %w", err)
	}
	return persistence.WriteJSONFile(path, entry)
}

// Lookup returns the entry for key, if one exists and matches it.
func Lookup(root string, key Key) (Entry, bool, error) {
	path, err := Path(root, key)
	if err != nil {
		return Entry{}, false, err
	}
	entry, err := persistence.ReadJSONFile[Entry](path)
	if errors.Is(err, os.ErrNotExist) {
		return Entry{}, false, nil
	}
	if err != nil {
		return Entry{}, false, err
	}
	if entry.Version != Version || entry.key() != key {
		return Entry{}, false, nil
	}
	return entry, true, nil
}

// Forget removes the entry for key, for example when its run no longer loads.
func Forget(root string, key Key) error {
	path, err := Path(root, key)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove result index entry: %w", err)
	}
	return nil
}

func (entry Entry) key() Key {
	return Key{RepoSlug: entry.RepoSlug, TestedTreeHash: entry.TestedTreeHash, ConfigHash: entry.ConfigHash, ConfigLocation: entry.ConfigLocation, SeedHash: entry.SeedHash, PlanHash: entry.PlanHash}
}

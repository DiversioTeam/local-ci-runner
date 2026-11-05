package resultcache

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	tree       = "1111111111111111111111111111111111111111"
	configHash = "2222222222222222222222222222222222222222222222222222222222222222"
	planHash   = "3333333333333333333333333333333333333333333333333333333333333333"
)

func newEntry() Entry {
	return Entry{
		RepoSlug: "owner/repo", RepoRoot: "/repo", RunID: "20260627T150405Z-deadbeef", RunDir: "/repo/.local-ci/runs/20260627T150405Z-deadbeef",
		HeadSHA: "4444444444444444444444444444444444444444", TestedTreeHash: tree, ConfigHash: configHash, PlanHash: planHash,
		FinishedAt:     time.Date(2026, 6, 27, 15, 4, 5, 0, time.UTC),
		ConfigLocation: ".local-ci.toml",
	}
}

func TestRecordThenLookupFindsOnlyTheSameKey(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	entry := newEntry()
	if err := Record(root, entry); err != nil {
		t.Fatal(err)
	}
	// The path is the documented layout, built literally rather than from Path.
	want := filepath.Join(root, "owner", "repo", tree, fmt.Sprintf("%s-%s-%x.json", configHash, planHash, sha256.Sum256([]byte(".local-ci.toml"))))
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("entry not at the documented path: %v", err)
	}
	got, found, err := Lookup(root, entry.key())
	if err != nil || !found || got.RunID != entry.RunID || got.Version != Version {
		t.Fatalf("Lookup() = %+v, %v, %v", got, found, err)
	}
	for name, key := range map[string]Key{
		"other location": {RepoSlug: "owner/repo", TestedTreeHash: tree, ConfigHash: configHash, ConfigLocation: "other/ci.toml", PlanHash: planHash},
		"other tree":     {RepoSlug: "owner/repo", TestedTreeHash: strings.Repeat("5", 40), ConfigHash: configHash, PlanHash: planHash},
		"other config":   {RepoSlug: "owner/repo", TestedTreeHash: tree, ConfigHash: strings.Repeat("5", 64), PlanHash: planHash},
		"other plan":     {RepoSlug: "owner/repo", TestedTreeHash: tree, ConfigHash: configHash, PlanHash: strings.Repeat("5", 64)},
		"other repo":     {RepoSlug: "owner/other", TestedTreeHash: tree, ConfigHash: configHash, PlanHash: planHash},
	} {
		if key.ConfigLocation == "" {
			key.ConfigLocation = ".local-ci.toml"
		}
		if _, found, err := Lookup(root, key); err != nil || found {
			t.Fatalf("%s: found = %v, error = %v", name, found, err)
		}
	}
	if err := Forget(root, entry.key()); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := Lookup(root, entry.key()); found {
		t.Fatal("Forget left the entry")
	}
}

func TestLookupIgnoresEntriesThatDoNotMatchTheirPath(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	entry := newEntry()
	path, err := Path(root, entry.key())
	if err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"future version":   `{"version":999,"config_location":".local-ci.toml","repo_slug":"owner/repo","tested_tree_hash":"` + tree + `","config_hash":"` + configHash + `","plan_hash":"` + planHash + `"}`,
		"copied elsewhere": `{"version":2,"config_location":".local-ci.toml","repo_slug":"owner/repo","tested_tree_hash":"` + strings.Repeat("9", 40) + `","config_hash":"` + configHash + `","plan_hash":"` + planHash + `"}`,
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, found, err := Lookup(root, entry.key()); err != nil || found {
			t.Fatalf("%s: found = %v, error = %v", name, found, err)
		}
	}
}

func TestPathRejectsKeysThatCouldEscapeTheIndex(t *testing.T) {
	t.Parallel()
	valid := newEntry().key()
	for name, mutate := range map[string]func(*Key){
		"unchanged":       func(*Key) {},
		"slug traversal":  func(key *Key) { key.RepoSlug = "../etc" },
		"nested slug":     func(key *Key) { key.RepoSlug = "owner/repo/extra" },
		"tree path":       func(key *Key) { key.TestedTreeHash = "../" + tree },
		"external config": func(key *Key) { key.ConfigLocation = "../ci.toml" },
		"missing config":  func(key *Key) { key.ConfigLocation = "" },
		"empty plan":      func(key *Key) { key.PlanHash = "" },
	} {
		key := valid
		mutate(&key)
		_, err := Path(t.TempDir(), key)
		if (name == "unchanged") != (err == nil) {
			t.Fatalf("%s: Path() error = %v", name, err)
		}
	}
}

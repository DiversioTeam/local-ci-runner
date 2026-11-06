package snapshot

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSeedCloneFailureRequiresExplicitCopyFallback(t *testing.T) {
	root := newRepo(t)
	options := newOptions(t, root)
	options.Seed = []string{".venv"}
	commands := t.TempDir()
	wrapper := filepath.Join(commands, "cp")
	if err := os.WriteFile(wrapper, []byte("#!/bin/sh\necho 'reflink unavailable' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", commands+string(os.PathListSeparator)+os.Getenv("PATH"))
	_, err := Create(t.Context(), options)
	if err == nil || !strings.Contains(err.Error(), "allow_plain_copy") {
		t.Fatalf("clone failure silently omitted dependencies: %v", err)
	}
	if _, err := os.Stat(Path(root, options.RunID)); !os.IsNotExist(err) {
		t.Fatalf("snapshot leaked after failed seed clone: %v", err)
	}
}

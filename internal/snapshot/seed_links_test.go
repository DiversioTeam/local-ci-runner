package snapshot

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSeedLinksCannotReachTheLiveWorktree(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"root link", "nested absolute", "nested relative escape", "internal relative", "external tool"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			root := newRepo(t)
			opts := newOptions(t, root)
			opts.AllowPlainCopy = true
			opts.Seed = []string{".venv"}
			link := filepath.Join(root, ".venv", "bin", "link")
			target := filepath.Join(root, "app.txt")
			wantFailure := true
			switch kind {
			case "root link":
				opts.Seed = []string{".env"}
				link = filepath.Join(root, ".env")
			case "nested relative escape":
				target = "../../../../../app.txt"
			case "internal relative":
				target = "tool"
				wantFailure = false
			case "external tool":
				target = filepath.Join(t.TempDir(), "python")
				writeFile(t, target, "external tool\n")
				wantFailure = false
			}
			if err := os.Symlink(target, link); err != nil {
				t.Fatal(err)
			}
			created, err := Create(t.Context(), opts)
			if wantFailure {
				if err == nil || !strings.Contains(err.Error(), "seed symlink") {
					t.Fatalf("Create() = %v, want seed symlink refusal", err)
				}
				if _, err := os.Lstat(Path(root, opts.RunID)); !os.IsNotExist(err) {
					t.Fatalf("refused snapshot remains: %v", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = Remove(root, created.Dir) })
				relativeLink, err := filepath.Rel(root, link)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := os.ReadFile(filepath.Join(created.Dir, relativeLink)); err != nil {
					t.Fatalf("safe seed link is unreadable: %v", err)
				}
			}
		})
	}
}

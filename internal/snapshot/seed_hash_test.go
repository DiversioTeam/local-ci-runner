package snapshot

import (
	"context"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func seedHash(t *testing.T, root string, paths ...string) string {
	t.Helper()
	hash, err := GetSeedHash(t.Context(), root, paths)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := hex.DecodeString(hash)
	if err != nil || len(decoded) != 32 {
		t.Fatalf("GetSeedHash() = %q, want SHA-256 hex", hash)
	}
	return hash
}

func TestGetSeedHashChangesWithSeedContents(t *testing.T) {
	t.Parallel()
	for _, change := range []string{"same size and mtime", "rename", "add", "remove", "file mode", "directory mode", "mtime only"} {
		t.Run(change, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			path := filepath.Join(root, "cache", "tool")
			writeFile(t, path, "before")
			if err := os.Chmod(path, 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			before := seedHash(t, root, "cache")
			switch change {
			case "same size and mtime":
				writeFile(t, path, "after!")
				err = os.Chtimes(path, info.ModTime(), info.ModTime())
			case "rename":
				err = os.Rename(path, filepath.Join(root, "cache", "renamed"))
			case "add":
				writeFile(t, filepath.Join(root, "cache", "added"), "new")
			case "remove":
				err = os.Remove(path)
			case "file mode":
				err = os.Chmod(path, 0o755)
			case "directory mode":
				err = os.Chmod(filepath.Dir(path), 0o700)
			case "mtime only":
				mtime := info.ModTime().Add(time.Hour)
				if err = os.Chtimes(path, mtime, mtime); err == nil {
					err = os.Chtimes(filepath.Dir(path), mtime, mtime)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			after := seedHash(t, root, "cache")
			if (before == after) != (change == "mtime only") {
				t.Fatalf("hash equality = %v after %s", before == after, change)
			}
		})
	}
}

func TestGetSeedHashAbsentAndEmpty(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	hashes := map[string]string{}
	checkDistinct := func(name string, paths ...string) {
		t.Helper()
		hash := seedHash(t, root, paths...)
		if previous, ok := hashes[hash]; ok {
			t.Fatalf("%s hashes like %s", name, previous)
		}
		hashes[hash] = name
	}
	checkDistinct("no seeds")
	if seedHash(t, root) != seedHash(t, t.TempDir()) {
		t.Fatal("empty seed hash depends on root")
	}
	checkDistinct("absent cache", "cache")
	checkDistinct("different absent seed", "other")
	checkDistinct("absent parent", "missing/child")
	if err := os.Mkdir(filepath.Join(root, "cache"), 0o755); err != nil {
		t.Fatal(err)
	}
	checkDistinct("empty directory", "cache")
	if err := os.Remove(filepath.Join(root, "cache")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "cache"), "")
	checkDistinct("empty regular file", "cache")
}

func TestGetSeedHashEquivalentRootsAndOrdering(t *testing.T) {
	t.Parallel()
	roots := []string{t.TempDir(), t.TempDir()}
	for i, root := range roots {
		files := []string{"cache/z", "cache/a", "config"}
		if i == 1 {
			files = []string{"config", "cache/a", "cache/z"}
		}
		for _, path := range files {
			writeFile(t, filepath.Join(root, path), strings.Repeat(path, 10000))
		}
		if err := os.Symlink("a", filepath.Join(root, "cache", "link")); err != nil {
			t.Fatal(err)
		}
	}
	paths := []string{"config", "cache"}
	before := append([]string(nil), paths...)
	want := seedHash(t, roots[0], paths...)
	if !reflect.DeepEqual(paths, before) {
		t.Fatal("GetSeedHash mutated caller seed paths")
	}
	if got := seedHash(t, roots[1], "cache", "config"); got != want {
		t.Fatal("equivalent roots and reordered paths hash differently")
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(roots[0], alias); err != nil {
		t.Fatal(err)
	}
	if got := seedHash(t, alias, paths...); got != want {
		t.Fatal("canonical root changes seed hash")
	}
}

func TestGetSeedHashRelativeSymlinks(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "cache", ".store", "tool"), "tool")
	if err := os.Mkdir(filepath.Join(root, "cache", ".bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "cache", ".bin", "tool")
	if err := os.Symlink("../.store/tool", link); err != nil {
		t.Fatal(err)
	}
	before := seedHash(t, root, "cache")
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../.store/./tool", link); err != nil {
		t.Fatal(err)
	}
	if after := seedHash(t, root, "cache"); before == after {
		t.Fatal("changed symlink text did not change hash")
	}

	// A target outside the seeds is covered by the tree hash, not traversal.
	writeFile(t, filepath.Join(root, "tracked", "tool"), "first")
	if err := os.Symlink("../tracked", filepath.Join(root, "cache", "tracked")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("..", filepath.Join(root, "cache", "cycle")); err != nil {
		t.Fatal(err)
	}
	before = seedHash(t, root, "cache")
	writeFile(t, filepath.Join(root, "tracked", "tool"), "second")
	if after := seedHash(t, root, "cache"); before != after {
		t.Fatal("hash followed directory symlink outside seeds")
	}
	seedHash(t, root, "cache/tracked") // A configured seed can itself be a relative link.
}

func TestGetSeedHashRejectsUnsafeSymlinks(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, rule string }{
		{"broken", "unresolved symlink"},
		{"external relative", "external symlink"},
		{"external absolute", "absolute symlink"},
		{"source absolute", "absolute symlink"},
		{"loop", "unresolved symlink"},
		{"ancestor", "parent"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			base := t.TempDir()
			root := filepath.Join(base, "repo")
			writeFile(t, filepath.Join(root, "cache", "tool"), "seed contents must not appear in errors")
			writeFile(t, filepath.Join(base, "external"), "outside")
			path := "cache/link"
			target := "missing"
			switch tc.name {
			case "external relative":
				target = "../../external"
			case "external absolute":
				target = filepath.Join(base, "external")
			case "source absolute":
				target = filepath.Join(root, "cache", "tool")
			case "loop":
				target = "link"
			case "ancestor":
				target = "../cache"
				path = "cache/link/tool"
			}
			if err := os.Symlink(target, filepath.Join(root, "cache", "link")); err != nil {
				t.Fatal(err)
			}
			hash, err := GetSeedHash(t.Context(), root, []string{path})
			if hash != "" || err == nil || !strings.Contains(err.Error(), tc.rule) || !strings.Contains(err.Error(), path) {
				t.Fatalf("GetSeedHash() = %q, %v; want path and %q refusal", hash, err, tc.rule)
			}
			if strings.Contains(err.Error(), "seed contents must not appear in errors") {
				t.Fatal("error exposed file contents")
			}
		})
	}
}

func TestGetSeedHashValidatesPaths(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for _, tc := range []struct {
		paths []string
		rule  string
	}{
		{[]string{""}, "clean relative path"},
		{[]string{"."}, "clean relative path"},
		{[]string{"/cache"}, "clean relative path"},
		{[]string{"../cache"}, "clean relative path"},
		{[]string{"cache/../other"}, "clean relative path"},
		{[]string{"cache//tool"}, "clean relative path"},
		{[]string{"cache/"}, "clean relative path"},
		{[]string{"cache\x00"}, "clean relative path"},
		{[]string{".git/objects"}, "runner owns"},
		{[]string{".local-ci"}, "runner owns"},
		{[]string{"cache", "cache"}, "overlaps"},
		{[]string{"cache/tool", "cache-other", "cache"}, "overlaps"},
		{[]string{"cache", "cache-other", "other/cache"}, ""},
	} {
		t.Run(strings.Join(tc.paths, ","), func(t *testing.T) {
			hash, err := GetSeedHash(t.Context(), root, tc.paths)
			if tc.rule == "" {
				if err != nil || hash == "" {
					t.Fatalf("valid paths: %q, %v", hash, err)
				}
			} else if hash != "" || err == nil || !strings.Contains(err.Error(), tc.rule) {
				t.Fatalf("GetSeedHash() = %q, %v; want %q refusal", hash, err, tc.rule)
			}
		})
	}
}

func TestGetSeedHashCancellation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, paths := range [][]string{nil, {"cache"}} {
		hash, err := GetSeedHash(ctx, t.TempDir(), paths)
		if hash != "" || !errors.Is(err, context.Canceled) {
			t.Fatalf("GetSeedHash() = %q, %v; want cancellation", hash, err)
		}
	}
}

func TestGetSeedHashRejectsSpecialFiles(t *testing.T) {
	t.Parallel()
	mkfifo, err := exec.LookPath("mkfifo")
	if err != nil {
		t.Skip("mkfifo unavailable")
	}
	root := t.TempDir()
	path := filepath.Join(root, "pipe")
	if output, err := exec.Command(mkfifo, path).CombinedOutput(); err != nil {
		t.Fatalf("mkfifo: %v: %s", err, output)
	}
	hash, err := GetSeedHash(t.Context(), root, []string{"pipe"})
	if hash != "" || err == nil || !strings.Contains(err.Error(), "special file") || !strings.Contains(err.Error(), "pipe") {
		t.Fatalf("GetSeedHash() = %q, %v; want special file refusal", hash, err)
	}
}

func TestGetSeedHashUnreadableFile(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := filepath.Join(root, "private")
	writeFile(t, path, "seed contents must not appear in errors")
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
	if file, err := os.Open(path); err == nil {
		file.Close()
		t.Skip("user can read mode-000 files")
	}
	hash, err := GetSeedHash(t.Context(), root, []string{"private"})
	if hash != "" || err == nil || !strings.Contains(err.Error(), "private") || !errors.Is(err, os.ErrPermission) {
		t.Fatalf("GetSeedHash() = %q, %v; want unreadable file refusal", hash, err)
	}
	if strings.Contains(err.Error(), "seed contents must not appear in errors") {
		t.Fatal("error exposed file contents")
	}
}

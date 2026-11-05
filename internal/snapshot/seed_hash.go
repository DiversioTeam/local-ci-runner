package snapshot

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// GetSeedHash fingerprints seed names, missing paths, modes, file contents and
// symlink text, without following directory symlinks or including timestamps.
// Unresolved, external and absolute symlinks cannot safely identify a snapshot.
func GetSeedHash(ctx context.Context, root string, seedPaths []string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("snapshot.seed root %q: %w", root, err)
	}
	paths := append([]string(nil), seedPaths...)
	sort.Strings(paths)
	seen := make(map[string]bool, len(paths))
	for _, path := range paths {
		if !filepath.IsLocal(path) || path == "." || filepath.Clean(path) != path || strings.ContainsRune(path, 0) {
			return "", fmt.Errorf("snapshot.seed %q must be a clean relative path inside the repository", path)
		}
		first := strings.Split(path, string(filepath.Separator))[0]
		if first == ".git" || first == ".local-ci" {
			return "", fmt.Errorf("snapshot.seed %q names a path the runner owns", path)
		}
		for parent := path; parent != "."; parent = filepath.Dir(parent) {
			if seen[parent] {
				return "", fmt.Errorf("snapshot.seed %q overlaps %q", path, parent)
			}
		}
		seen[path] = true
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("snapshot.seed root %q: %w", root, err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("snapshot.seed canonical root: %w", err)
	}
	rootInfo, err := os.Stat(root)
	if err != nil {
		return "", fmt.Errorf("snapshot.seed root %q: %w", root, err)
	}
	if !rootInfo.IsDir() {
		return "", fmt.Errorf("snapshot.seed root %q must be a directory", root)
	}

	hash := sha256.New()
	fmt.Fprintln(hash, "snapshot.seed.v1")
	buffer := make([]byte, 32*1024)
	for _, seedPath := range paths {
		if err := ctx.Err(); err != nil {
			return "", fmt.Errorf("snapshot.seed %q: %w", seedPath, err)
		}
		// WalkDir does not follow links, but its starting path's parents could.
		parent := root
		parts := strings.Split(seedPath, string(filepath.Separator))
		for _, part := range parts[:len(parts)-1] {
			parent = filepath.Join(parent, part)
			info, err := os.Lstat(parent)
			if errors.Is(err, os.ErrNotExist) {
				break
			}
			if err != nil {
				return "", fmt.Errorf("snapshot.seed %q: inspect parent: %w", seedPath, err)
			}
			if !info.IsDir() {
				return "", fmt.Errorf("snapshot.seed %q: parent %q must be a directory, not a symlink or special file", seedPath, parent)
			}
		}
		fmt.Fprintf(hash, "seed %q\n", seedPath)
		err := filepath.WalkDir(filepath.Join(root, seedPath), func(path string, entry fs.DirEntry, walkErr error) error {
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			if err := ctx.Err(); err != nil {
				return fmt.Errorf("path %q: %w", rel, err)
			}
			if walkErr != nil {
				if entry == nil && rel == seedPath && errors.Is(walkErr, os.ErrNotExist) {
					fmt.Fprintln(hash, "absent")
					return nil
				}
				return fmt.Errorf("path %q: walk: %w", rel, walkErr)
			}
			info, err := entry.Info()
			if err != nil {
				return fmt.Errorf("path %q: stat: %w", rel, err)
			}
			fmt.Fprintf(hash, "%q %o ", rel, info.Mode())
			switch {
			case info.IsDir():
				fmt.Fprintln(hash, "directory")
			case info.Mode()&os.ModeSymlink != 0:
				text, err := os.Readlink(path)
				if err != nil {
					return fmt.Errorf("path %q: read symlink: %w", rel, err)
				}
				if filepath.IsAbs(text) {
					return fmt.Errorf("path %q: absolute symlink is not fingerprintable", rel)
				}
				target, err := filepath.EvalSymlinks(path)
				if err != nil {
					return fmt.Errorf("path %q: unresolved symlink: %w", rel, err)
				}
				targetRel, err := filepath.Rel(root, target)
				if err != nil || !filepath.IsLocal(targetRel) {
					return fmt.Errorf("path %q: external symlink is not fingerprintable", rel)
				}
				fmt.Fprintf(hash, "symlink %q\n", text)
			case info.Mode().IsRegular():
				digest, err := hashSeedFile(ctx, path, info, buffer)
				if err != nil {
					return fmt.Errorf("path %q: hash regular file: %w", rel, err)
				}
				fmt.Fprintf(hash, "file %x\n", digest)
			default:
				return fmt.Errorf("path %q: special file is not fingerprintable", rel)
			}
			return nil
		})
		if err != nil {
			return "", fmt.Errorf("snapshot.seed %q: %w", seedPath, err)
		}
	}
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("snapshot.seed root %q: %w", root, err)
	}
	return fmt.Sprintf("%x", hash.Sum(nil)), nil
}

func hashSeedFile(ctx context.Context, path string, info fs.FileInfo, buffer []byte) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(info, opened) || info.Mode() != opened.Mode() || info.Size() != opened.Size() {
		return nil, fmt.Errorf("file changed while opening")
	}
	hash := sha256.New()
	var size int64
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		n, err := file.Read(buffer)
		hash.Write(buffer[:n])
		size += int64(n)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
	}
	if size != info.Size() {
		return nil, fmt.Errorf("file size changed while hashing")
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	return hash.Sum(nil), nil
}

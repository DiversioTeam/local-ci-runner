package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func buildReleaseArchive(t *testing.T, contents string) []byte {
	t.Helper()

	var gzipped bytes.Buffer
	gzipWriter := gzip.NewWriter(&gzipped)
	tarWriter := tar.NewWriter(gzipWriter)
	header := &tar.Header{
		Name:     binaryName,
		Mode:     0o755,
		Size:     int64(len(contents)),
		Typeflag: tar.TypeReg,
	}
	if err := tarWriter.WriteHeader(header); err != nil {
		t.Fatalf("write tar header: %v", err)
	}
	if _, err := tarWriter.Write([]byte(contents)); err != nil {
		t.Fatalf("write tar body: %v", err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	return gzipped.Bytes()
}

// releaseServer serves a latest-release document plus the archive and
// checksums for one platform, mirroring the real release layout.
func releaseServer(t *testing.T, version string, archive []byte, corruptChecksum bool) *httptest.Server {
	t.Helper()

	archiveName := fmt.Sprintf("%s_%s_testos_testarch.tar.gz", binaryName, strings.TrimPrefix(version, "v"))
	sum := sha256.Sum256(archive)
	digest := hex.EncodeToString(sum[:])
	if corruptChecksum {
		digest = strings.Repeat("0", len(digest))
	}
	checksums := fmt.Sprintf("%s  %s\n%s  other_file.tar.gz\n", digest, archiveName, strings.Repeat("a", 64))

	mux := http.NewServeMux()
	mux.HandleFunc("/releases/latest", func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(writer, `{"tag_name":%q,"html_url":"https://example.com/release"}`, version)
	})
	mux.HandleFunc("/download/"+version+"/"+archiveName, func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write(archive)
	})
	mux.HandleFunc("/download/"+version+"/checksums.txt", func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(checksums))
	})
	return httptest.NewServer(mux)
}

func newTestUpdater(t *testing.T, server *httptest.Server, currentVersion string, executablePath string) Updater {
	t.Helper()

	return Updater{
		Checker: Checker{
			CurrentVersion:   currentVersion,
			LatestReleaseURL: server.URL + "/releases/latest",
			CachePath:        filepath.Join(t.TempDir(), "update.json"),
			ExecutablePath:   executablePath,
		},
		Stdout:          &bytes.Buffer{},
		DownloadBaseURL: server.URL + "/download",
		Platform:        "testos_testarch",
	}
}

func TestUpdaterApplyReplacesBinary(t *testing.T) {
	t.Parallel()

	archive := buildReleaseArchive(t, "new-binary-contents")
	server := releaseServer(t, "v0.2.0", archive, false)
	defer server.Close()

	executablePath := filepath.Join(t.TempDir(), "local-ci")
	if err := os.WriteFile(executablePath, []byte("old-binary-contents"), 0o755); err != nil {
		t.Fatalf("seed binary: %v", err)
	}

	updater := newTestUpdater(t, server, "v0.1.0", executablePath)
	if err := updater.Apply(context.Background()); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}

	got, err := os.ReadFile(executablePath)
	if err != nil {
		t.Fatalf("read binary: %v", err)
	}
	if string(got) != "new-binary-contents" {
		t.Fatalf("binary contents = %q, want the downloaded binary", string(got))
	}
	info, err := os.Stat(executablePath)
	if err != nil {
		t.Fatalf("stat binary: %v", err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("binary mode = %v, want 0755", info.Mode().Perm())
	}
}

func TestUpdaterApplyRejectsChecksumMismatch(t *testing.T) {
	t.Parallel()

	archive := buildReleaseArchive(t, "tampered-binary")
	server := releaseServer(t, "v0.2.0", archive, true)
	defer server.Close()

	executablePath := filepath.Join(t.TempDir(), "local-ci")
	if err := os.WriteFile(executablePath, []byte("old-binary-contents"), 0o755); err != nil {
		t.Fatalf("seed binary: %v", err)
	}

	updater := newTestUpdater(t, server, "v0.1.0", executablePath)
	err := updater.Apply(context.Background())
	if err == nil {
		t.Fatal("expected a checksum mismatch error")
	}
	if !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("error = %v, want a checksum mismatch", err)
	}

	got, err := os.ReadFile(executablePath)
	if err != nil {
		t.Fatalf("read binary: %v", err)
	}
	if string(got) != "old-binary-contents" {
		t.Fatalf("binary contents = %q, want the original binary left in place", string(got))
	}
	entries, err := os.ReadDir(filepath.Dir(executablePath))
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("directory entries = %d, want only the untouched binary", len(entries))
	}
}

func TestUpdaterApplyNoopWhenCurrent(t *testing.T) {
	t.Parallel()

	archive := buildReleaseArchive(t, "new-binary-contents")
	server := releaseServer(t, "v0.2.0", archive, false)
	defer server.Close()

	executablePath := filepath.Join(t.TempDir(), "local-ci")
	if err := os.WriteFile(executablePath, []byte("current-binary"), 0o755); err != nil {
		t.Fatalf("seed binary: %v", err)
	}

	updater := newTestUpdater(t, server, "v0.2.0", executablePath)
	output := &bytes.Buffer{}
	updater.Stdout = output
	if err := updater.Apply(context.Background()); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if !strings.Contains(output.String(), "already the latest version") {
		t.Fatalf("output = %q, want an already-current message", output.String())
	}
	got, err := os.ReadFile(executablePath)
	if err != nil {
		t.Fatalf("read binary: %v", err)
	}
	if string(got) != "current-binary" {
		t.Fatalf("binary contents = %q, want it untouched", string(got))
	}
}

func TestUpdaterApplyRejectsDevelopmentBuild(t *testing.T) {
	t.Parallel()

	updater := Updater{Checker: Checker{CurrentVersion: DefaultVersion}, Stdout: &bytes.Buffer{}}
	err := updater.Apply(context.Background())
	if err == nil {
		t.Fatal("expected a development build error")
	}
	if !strings.Contains(err.Error(), "development build") {
		t.Fatalf("error = %v, want a development build error", err)
	}
}

func TestExtractBinaryRejectsArchiveWithoutBinary(t *testing.T) {
	t.Parallel()

	var gzipped bytes.Buffer
	gzipWriter := gzip.NewWriter(&gzipped)
	tarWriter := tar.NewWriter(gzipWriter)
	header := &tar.Header{Name: "README.md", Mode: 0o644, Size: 2, Typeflag: tar.TypeReg}
	if err := tarWriter.WriteHeader(header); err != nil {
		t.Fatalf("write tar header: %v", err)
	}
	if _, err := tarWriter.Write([]byte("hi")); err != nil {
		t.Fatalf("write tar body: %v", err)
	}
	_ = tarWriter.Close()
	_ = gzipWriter.Close()

	if _, err := extractBinary(gzipped.Bytes()); err == nil {
		t.Fatal("expected an error for an archive with no local-ci binary")
	}
}

// TestUpdaterApplyToleratesSlowDownload pins the bug that the release download
// must not inherit the update notice's ~1s budget. Updates always use the client
// the CLI gets; an injectable test client is what hid the problem in the first place.
func TestUpdaterApplyToleratesSlowDownload(t *testing.T) {
	t.Parallel()

	archive := buildReleaseArchive(t, "new-binary-contents")
	archiveName := "local-ci_0.2.0_testos_testarch.tar.gz"
	sum := sha256.Sum256(archive)
	checksums := fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), archiveName)

	mux := http.NewServeMux()
	mux.HandleFunc("/releases/latest", func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"tag_name":"v0.2.0"}`))
	})
	mux.HandleFunc("/download/v0.2.0/"+archiveName, func(writer http.ResponseWriter, _ *http.Request) {
		// Longer than the notice budget, far below the update budget.
		time.Sleep(1500 * time.Millisecond)
		_, _ = writer.Write(archive)
	})
	mux.HandleFunc("/download/v0.2.0/checksums.txt", func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(checksums))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	executablePath := filepath.Join(t.TempDir(), "local-ci")
	if err := os.WriteFile(executablePath, []byte("old-binary-contents"), 0o755); err != nil {
		t.Fatalf("seed binary: %v", err)
	}

	updater := Updater{
		Checker: Checker{
			CurrentVersion:   "v0.1.0",
			LatestReleaseURL: server.URL + "/releases/latest",
			CachePath:        filepath.Join(t.TempDir(), "update.json"),
			ExecutablePath:   executablePath,
		},
		Stdout:          &bytes.Buffer{},
		DownloadBaseURL: server.URL + "/download",
		Platform:        "testos_testarch",
	}
	if err := updater.Apply(context.Background()); err != nil {
		t.Fatalf("Apply() error = %v, want a slow download to succeed", err)
	}

	got, err := os.ReadFile(executablePath)
	if err != nil {
		t.Fatalf("read binary: %v", err)
	}
	if string(got) != "new-binary-contents" {
		t.Fatalf("binary contents = %q, want the downloaded binary", string(got))
	}
}

// newHomebrewInstall returns the PATH entry Homebrew would create: a symlink to a binary in the
// Cellar. Only resolving that symlink reveals a Homebrew install.
func newHomebrewInstall(t *testing.T) string {
	t.Helper()

	prefix := t.TempDir()
	cellarBinary := filepath.Join(prefix, "Cellar", "local-ci", "0.1.0", "bin", "local-ci")
	if err := os.MkdirAll(filepath.Dir(cellarBinary), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cellarBinary, []byte("brew-managed"), 0o755); err != nil {
		t.Fatal(err)
	}
	linkPath := filepath.Join(prefix, "bin", "local-ci")
	if err := os.MkdirAll(filepath.Dir(linkPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(cellarBinary, linkPath); err != nil {
		t.Fatal(err)
	}
	return linkPath
}

// The Homebrew tests change PATH, so they cannot run in parallel.
func TestUpdaterApplyUsesHomebrewForCellarInstall(t *testing.T) {
	server := releaseServer(t, "v0.2.0", buildReleaseArchive(t, "new-binary-contents"), false)
	defer server.Close()
	brewDir := t.TempDir()
	brewLog := filepath.Join(t.TempDir(), "brew.log")
	if err := os.WriteFile(filepath.Join(brewDir, "brew"), []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$BREW_LOG\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", brewDir)
	t.Setenv("BREW_LOG", brewLog)
	linkPath := newHomebrewInstall(t)

	if err := newTestUpdater(t, server, "v0.1.0", linkPath).Apply(context.Background()); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	// brew upgrade only sees the new version after the tap is refreshed.
	if got, err := os.ReadFile(brewLog); err != nil || string(got) != "update\nupgrade local-ci\n" {
		t.Fatalf("brew calls = %q, error = %v", got, err)
	}
	if got, _ := os.ReadFile(linkPath); string(got) != "brew-managed" {
		t.Fatal("a Homebrew install was overwritten instead of handed to brew")
	}
}

func TestUpdaterApplyReportsMissingHomebrew(t *testing.T) {
	server := releaseServer(t, "v0.2.0", buildReleaseArchive(t, "new-binary-contents"), false)
	defer server.Close()
	t.Setenv("PATH", t.TempDir())

	err := newTestUpdater(t, server, "v0.1.0", newHomebrewInstall(t)).Apply(context.Background())
	if err == nil || !strings.Contains(err.Error(), "brew is not on PATH") {
		t.Fatalf("Apply() error = %v, want it to explain that brew is missing", err)
	}
}

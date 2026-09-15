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
			HTTPClient:       server.Client(),
			ExecutablePath:   executablePath,
			Now:              func() time.Time { return time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC) },
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

func TestUpdaterApplyUsesHomebrewForCellarInstall(t *testing.T) {
	t.Parallel()

	archive := buildReleaseArchive(t, "new-binary-contents")
	server := releaseServer(t, "v0.2.0", archive, false)
	defer server.Close()

	executablePath := "/opt/homebrew/Cellar/local-ci/0.1.0/bin/local-ci"
	updater := newTestUpdater(t, server, "v0.1.0", executablePath)

	var commands [][]string
	updater.RunCommand = func(_ context.Context, name string, args ...string) error {
		commands = append(commands, append([]string{name}, args...))
		return nil
	}
	if err := updater.Apply(context.Background()); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}

	want := [][]string{{"brew", "update"}, {"brew", "upgrade", "local-ci"}}
	if fmt.Sprint(commands) != fmt.Sprint(want) {
		t.Fatalf("commands = %v, want %v", commands, want)
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

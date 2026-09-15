package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const binaryName = "local-ci"

// maxDownloadSize caps every download so a malformed or hostile response
// cannot exhaust memory. Release binaries are a few MB; this is generous.
const maxDownloadSize = 256 << 20

// Updater moves the running binary to the latest published release.
//
// The zero value works. The remaining fields exist so tests can point the
// updater at a local server and a scratch platform instead of the real ones.
type Updater struct {
	Checker Checker
	Stdout  io.Writer

	// DownloadBaseURL is where release archives live, without the tag.
	DownloadBaseURL string
	// Platform names the release build to fetch, such as "darwin_arm64".
	Platform string
	// RunCommand runs an external command, so the brew path can be tested
	// without a Homebrew installation.
	RunCommand func(ctx context.Context, name string, args ...string) error
}

// Apply updates the running binary to the latest release.
//
// How it updates depends on where the binary lives. A Homebrew install has to
// go back through brew, otherwise brew's own records would still describe the
// old version. Every other install is just a file we can swap ourselves.
func (updater Updater) Apply(ctx context.Context) error {
	currentVersion := updater.Checker.currentVersion()
	if currentVersion == DefaultVersion {
		return errors.New("this is a development build; rebuild from source instead of running 'local-ci update'")
	}

	latestVersion, err := updater.latestVersion(ctx)
	if err != nil {
		return err
	}
	if !isNewerVersion(currentVersion, latestVersion) {
		updater.printf("local-ci %s is already the latest version\n", currentVersion)
		return nil
	}

	executablePath, err := updater.Checker.executablePath()
	if err != nil {
		return err
	}

	updater.printf("updating local-ci %s -> %s\n", currentVersion, latestVersion)
	if isHomebrewPath(executablePath) {
		return updater.upgradeWithHomebrew(ctx)
	}
	return updater.replaceWithRelease(ctx, latestVersion, executablePath)
}

// latestVersion asks GitHub for the current release tag.
func (updater Updater) latestVersion(ctx context.Context) (string, error) {
	// Deliberately skips the notice cache. A 12-hour-old answer is fine for a
	// passive hint, but someone asking to update wants today's release.
	entry, err := updater.Checker.fetchLatest(ctx)
	if err != nil {
		return "", err
	}
	version := strings.TrimSpace(entry.LatestVersion)
	if version == "" {
		return "", errors.New("no published release found")
	}
	return version, nil
}

func (updater Updater) upgradeWithHomebrew(ctx context.Context) error {
	if _, err := exec.LookPath("brew"); err != nil {
		return fmt.Errorf("this looks like a Homebrew install but brew is not on PATH; run: %s", brewUpgradeCommand)
	}
	updater.printf("detected a Homebrew install; handing over to brew\n")

	// brew upgrade only sees the new version once the tap has been refreshed.
	if err := updater.runCommand(ctx, "brew", "update"); err != nil {
		return fmt.Errorf("brew update: %w", err)
	}
	if err := updater.runCommand(ctx, "brew", "upgrade", binaryName); err != nil {
		return fmt.Errorf("brew upgrade %s: %w", binaryName, err)
	}
	return nil
}

// replaceWithRelease downloads the release built for this machine, checks it
// against the published checksum, and swaps it in for the running binary.
func (updater Updater) replaceWithRelease(ctx context.Context, version string, executablePath string) error {
	archiveName := fmt.Sprintf("%s_%s_%s.tar.gz", binaryName, strings.TrimPrefix(version, "v"), updater.platform())
	releaseURL := strings.TrimSuffix(updater.downloadBaseURL(), "/") + "/" + version

	archive, err := updater.download(ctx, releaseURL+"/"+archiveName)
	if err != nil {
		return err
	}
	checksums, err := updater.download(ctx, releaseURL+"/checksums.txt")
	if err != nil {
		return err
	}

	// Verify before unpacking, so nothing reaches disk unless the bytes match
	// what the release published.
	if err := verifyChecksum(archive, checksums, archiveName); err != nil {
		return err
	}
	binary, err := extractBinary(archive)
	if err != nil {
		return err
	}
	if err := replaceFile(executablePath, binary); err != nil {
		return err
	}

	updater.printf("updated %s to %s\n", executablePath, version)
	return nil
}

func (updater Updater) download(ctx context.Context, url string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build request for %s: %w", url, err)
	}
	request.Header.Set("User-Agent", "local-ci-runner/"+updater.Checker.currentVersion())

	response, err := updater.Checker.httpClient().Do(request)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", url, err)
	}
	defer func() {
		_ = response.Body.Close()
	}()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download %s: %s", url, response.Status)
	}
	return io.ReadAll(io.LimitReader(response.Body, maxDownloadSize))
}

// verifyChecksum compares the archive against its line in checksums.txt, whose
// format is "<sha256>  <file name>", one release archive per line.
func verifyChecksum(archive []byte, checksums []byte, archiveName string) error {
	expected := ""
	for _, line := range strings.Split(string(checksums), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == archiveName {
			expected = fields[0]
			break
		}
	}
	// An archive with no published checksum is not something to install.
	if expected == "" {
		return fmt.Errorf("no checksum published for %s", archiveName)
	}

	actual := sha256.Sum256(archive)
	if hex.EncodeToString(actual[:]) != expected {
		return fmt.Errorf(
			"checksum mismatch for %s: expected %s, got %s",
			archiveName, expected, hex.EncodeToString(actual[:]),
		)
	}
	return nil
}

// extractBinary pulls the local-ci binary out of a release archive.
func extractBinary(archive []byte) ([]byte, error) {
	gzipReader, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fmt.Errorf("open release archive: %w", err)
	}
	defer func() {
		_ = gzipReader.Close()
	}()

	tarReader := tar.NewReader(gzipReader)
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("release archive did not contain a %s binary", binaryName)
		}
		if err != nil {
			return nil, fmt.Errorf("read release archive: %w", err)
		}
		// Matching on the base name keeps a crafted entry name harmless: the
		// name from the archive is never joined onto a filesystem path.
		if header.Typeflag != tar.TypeReg || filepath.Base(header.Name) != binaryName {
			continue
		}
		return io.ReadAll(io.LimitReader(tarReader, maxDownloadSize))
	}
}

// replaceFile swaps new contents in for the file at path.
//
// The new bytes are staged as a sibling and renamed over the target. Staging
// in the same directory keeps both files on one filesystem, so the rename is
// atomic: a failure part way through leaves the old binary in place rather
// than a half-written one on PATH. Renaming over a running binary is safe on
// Unix, because the running process holds the old inode open.
func replaceFile(path string, contents []byte) error {
	staged, err := os.CreateTemp(filepath.Dir(path), ".local-ci-update-*")
	if err != nil {
		return describeWriteError(err, path)
	}
	stagedPath := staged.Name()
	// Harmless once the rename succeeds and the staged path no longer exists.
	defer func() {
		_ = os.Remove(stagedPath)
	}()

	if _, err := staged.Write(contents); err != nil {
		_ = staged.Close()
		return fmt.Errorf("write %s: %w", stagedPath, err)
	}
	if err := staged.Close(); err != nil {
		return fmt.Errorf("close %s: %w", stagedPath, err)
	}
	// CreateTemp makes the file 0600; an executable needs the usual mode.
	if err := os.Chmod(stagedPath, 0o755); err != nil {
		return fmt.Errorf("chmod %s: %w", stagedPath, err)
	}
	if err := os.Rename(stagedPath, path); err != nil {
		return describeWriteError(err, path)
	}
	return nil
}

// describeWriteError turns a permission failure into the action that fixes it,
// since installing into a root-owned directory is a normal thing to have done.
func describeWriteError(err error, path string) error {
	if errors.Is(err, fs.ErrPermission) {
		return fmt.Errorf(
			"cannot replace %s: permission denied; re-run as a user that owns it, for example: sudo local-ci update",
			path,
		)
	}
	return fmt.Errorf("replace %s: %w", path, err)
}

func (updater Updater) runCommand(ctx context.Context, name string, args ...string) error {
	if updater.RunCommand != nil {
		return updater.RunCommand(ctx, name, args...)
	}
	command := exec.CommandContext(ctx, name, args...)
	// brew reports progress on both streams; the operator should see it.
	command.Stdout = updater.stdout()
	command.Stderr = updater.stdout()
	return command.Run()
}

func (updater Updater) downloadBaseURL() string {
	if strings.TrimSpace(updater.DownloadBaseURL) != "" {
		return updater.DownloadBaseURL
	}
	return fmt.Sprintf("https://github.com/%s/releases/download", updater.Checker.repo())
}

// platform matches the os_arch suffix the release workflow builds archives for.
func (updater Updater) platform() string {
	if strings.TrimSpace(updater.Platform) != "" {
		return updater.Platform
	}
	return runtime.GOOS + "_" + runtime.GOARCH
}

func (updater Updater) stdout() io.Writer {
	if updater.Stdout != nil {
		return updater.Stdout
	}
	return os.Stdout
}

func (updater Updater) printf(format string, args ...any) {
	_, _ = fmt.Fprintf(updater.stdout(), format, args...)
}

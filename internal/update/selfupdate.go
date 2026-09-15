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

// maxBinarySize caps what is read out of a release archive so a malformed or
// hostile tarball cannot exhaust memory or disk.
const maxBinarySize = 256 << 20

const binaryName = "local-ci"

// Method is how the running binary was installed, which decides how it updates.
type Method string

const (
	MethodHomebrew Method = "homebrew"
	MethodBinary   Method = "binary"
)

// Updater replaces the running binary with the latest published release.
type Updater struct {
	Checker         Checker
	Stdout          io.Writer
	DownloadBaseURL string
	GOOS            string
	GOARCH          string
	RunCommand      func(ctx context.Context, name string, args ...string) error
}

// Apply updates the running binary in place, or shells out to Homebrew when
// the binary came from a Homebrew install.
func (updater Updater) Apply(ctx context.Context) error {
	currentVersion := updater.Checker.currentVersion()
	if currentVersion == DefaultVersion {
		return errors.New("this is a development build; rebuild from source instead of running 'local-ci update'")
	}

	// Always ask GitHub directly: the notice cache may be up to 12h stale, and
	// an explicit update request should act on the current release.
	entry, err := updater.Checker.fetchLatest(ctx)
	if err != nil {
		return err
	}
	if strings.TrimSpace(entry.LatestVersion) == "" {
		return errors.New("no published release found")
	}
	if !isNewerVersion(currentVersion, entry.LatestVersion) {
		updater.printf("local-ci %s is already the latest version\n", currentVersion)
		return nil
	}

	executablePath, err := updater.Checker.executablePath()
	if err != nil {
		return err
	}
	updater.printf("updating local-ci %s -> %s\n", currentVersion, entry.LatestVersion)

	if DetectMethod(executablePath) == MethodHomebrew {
		return updater.applyHomebrew(ctx)
	}
	return updater.applyBinary(ctx, entry.LatestVersion, executablePath)
}

// DetectMethod reports how the binary at executablePath was installed.
func DetectMethod(executablePath string) Method {
	if isHomebrewPath(executablePath) {
		return MethodHomebrew
	}
	return MethodBinary
}

func (updater Updater) applyHomebrew(ctx context.Context) error {
	if _, err := exec.LookPath("brew"); err != nil {
		return fmt.Errorf("this looks like a Homebrew install but brew is not on PATH; run: %s", brewUpgradeCommand)
	}
	updater.printf("detected a Homebrew install; handing over to brew\n")
	if err := updater.runCommand(ctx, "brew", "update"); err != nil {
		return fmt.Errorf("brew update: %w", err)
	}
	if err := updater.runCommand(ctx, "brew", "upgrade", binaryName); err != nil {
		return fmt.Errorf("brew upgrade %s: %w", binaryName, err)
	}
	return nil
}

func (updater Updater) applyBinary(ctx context.Context, version string, executablePath string) error {
	archiveName := fmt.Sprintf(
		"%s_%s_%s_%s.tar.gz",
		binaryName,
		strings.TrimPrefix(version, "v"),
		updater.goos(),
		updater.goarch(),
	)
	baseURL := strings.TrimSuffix(updater.downloadBaseURL(), "/") + "/" + version

	archive, err := updater.download(ctx, baseURL+"/"+archiveName)
	if err != nil {
		return err
	}
	checksums, err := updater.download(ctx, baseURL+"/checksums.txt")
	if err != nil {
		return err
	}
	if err := verifyChecksum(archive, checksums, archiveName); err != nil {
		return err
	}
	binary, err := extractBinary(archive)
	if err != nil {
		return err
	}
	if err := replaceBinary(executablePath, binary); err != nil {
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
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxBinarySize))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", url, err)
	}
	return payload, nil
}

func verifyChecksum(archive []byte, checksums []byte, archiveName string) error {
	expected := ""
	for _, line := range strings.Split(string(checksums), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == archiveName {
			expected = fields[0]
			break
		}
	}
	if expected == "" {
		return fmt.Errorf("no checksum published for %s", archiveName)
	}
	sum := sha256.Sum256(archive)
	actual := hex.EncodeToString(sum[:])
	if actual != expected {
		return fmt.Errorf("checksum mismatch for %s: expected %s, got %s", archiveName, expected, actual)
	}
	return nil
}

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
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read release archive: %w", err)
		}
		// Match on the base name only; the archive path is never joined onto a
		// filesystem path, so a crafted entry name cannot escape anywhere.
		if header.Typeflag != tar.TypeReg || filepath.Base(header.Name) != binaryName {
			continue
		}
		binary, err := io.ReadAll(io.LimitReader(tarReader, maxBinarySize))
		if err != nil {
			return nil, fmt.Errorf("read %s from release archive: %w", binaryName, err)
		}
		return binary, nil
	}
	return nil, fmt.Errorf("release archive did not contain a %s binary", binaryName)
}

func replaceBinary(executablePath string, binary []byte) error {
	targetDir := filepath.Dir(executablePath)
	// Staging in the target directory keeps the swap on one filesystem, so the
	// rename is atomic and never leaves a half-written binary on PATH.
	temporaryFile, err := os.CreateTemp(targetDir, ".local-ci-update-*")
	if err != nil {
		return describePermissionError(err, executablePath)
	}
	temporaryPath := temporaryFile.Name()
	defer func() {
		_ = os.Remove(temporaryPath)
	}()

	if _, err := temporaryFile.Write(binary); err != nil {
		_ = temporaryFile.Close()
		return fmt.Errorf("write %s: %w", temporaryPath, err)
	}
	if err := temporaryFile.Close(); err != nil {
		return fmt.Errorf("close %s: %w", temporaryPath, err)
	}
	if err := os.Chmod(temporaryPath, 0o755); err != nil {
		return fmt.Errorf("chmod %s: %w", temporaryPath, err)
	}
	// Renaming over the running binary is safe on Unix: the running process
	// keeps the old inode open while the path points at the new file.
	if err := os.Rename(temporaryPath, executablePath); err != nil {
		return describePermissionError(err, executablePath)
	}
	return nil
}

func describePermissionError(err error, executablePath string) error {
	if errors.Is(err, fs.ErrPermission) {
		return fmt.Errorf(
			"cannot replace %s: permission denied; re-run as a user that owns it, for example: sudo local-ci update",
			executablePath,
		)
	}
	return fmt.Errorf("replace %s: %w", executablePath, err)
}

func (updater Updater) runCommand(ctx context.Context, name string, args ...string) error {
	if updater.RunCommand != nil {
		return updater.RunCommand(ctx, name, args...)
	}
	command := exec.CommandContext(ctx, name, args...)
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

func (updater Updater) goos() string {
	if strings.TrimSpace(updater.GOOS) != "" {
		return updater.GOOS
	}
	return runtime.GOOS
}

func (updater Updater) goarch() string {
	if strings.TrimSpace(updater.GOARCH) != "" {
		return updater.GOARCH
	}
	return runtime.GOARCH
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

package update

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	releaseDownloadBase = "https://github.com/" + Repository + "/releases/download"
	maxChecksumsBytes   = 1 << 20
	maxArchiveBytes     = 128 << 20
	maxBinaryBytes      = 128 << 20
	maxUnpackedBytes    = 256 << 20
)

// ErrManualUpdate means this installation cannot be replaced safely by the
// running process. The caller should show ManualCommand instead.
var ErrManualUpdate = errors.New("automatic replacement is unavailable")

// Installer downloads, verifies, and atomically replaces an installed binary.
type Installer struct {
	client         *http.Client
	downloadBase   string
	executablePath string
	goos           string
	goarch         string
	rename         func(string, string) error
}

// NewInstaller creates an installer restricted to the official GitHub release
// hosts and current executable.
func NewInstaller() Installer {
	client := &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			host := strings.ToLower(req.URL.Hostname())
			if host != "github.com" && !strings.HasSuffix(host, ".githubusercontent.com") {
				return fmt.Errorf("refusing release redirect to %q", host)
			}
			if len(via) >= 10 {
				return errors.New("too many release redirects")
			}
			return nil
		},
	}
	return Installer{
		client:       client,
		downloadBase: releaseDownloadBase,
		goos:         runtime.GOOS,
		goarch:       runtime.GOARCH,
		rename:       os.Rename,
	}
}

// ManualCommand returns the supported installer command for a release.
func ManualCommand(version string) string {
	return "curl -fsSL https://raw.githubusercontent.com/xautjzd/agent-cli/main/install.sh | bash -s -- --version " + version
}

// Install verifies the release archive against checksums.txt and atomically
// replaces the current executable. A failure before rename leaves it untouched.
func (i Installer) Install(ctx context.Context, release Release) error {
	version, ok := parseVersion(release.Version)
	if !ok || release.Tag != "v"+version.String() {
		return fmt.Errorf("invalid release version %q", release.Version)
	}
	if i.goos == "" {
		i.goos = runtime.GOOS
	}
	if i.goarch == "" {
		i.goarch = runtime.GOARCH
	}
	if i.goos == "windows" {
		return fmt.Errorf("%w on Windows", ErrManualUpdate)
	}
	if i.goos != "darwin" && i.goos != "linux" {
		return fmt.Errorf("%w on %s", ErrManualUpdate, i.goos)
	}
	if i.goarch != "amd64" && i.goarch != "arm64" {
		return fmt.Errorf("%w on %s/%s", ErrManualUpdate, i.goos, i.goarch)
	}
	if i.client == nil {
		i.client = NewInstaller().client
	}
	if i.downloadBase == "" {
		i.downloadBase = releaseDownloadBase
	}
	if i.rename == nil {
		i.rename = os.Rename
	}

	executable := i.executablePath
	if executable == "" {
		var err error
		executable, err = os.Executable()
		if err != nil {
			return fmt.Errorf("%w: locate executable: %v", ErrManualUpdate, err)
		}
	}
	resolved, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return fmt.Errorf("%w: resolve executable: %v", ErrManualUpdate, err)
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("%w: executable is not a regular file", ErrManualUpdate)
	}

	archiveName := fmt.Sprintf("agent-cli_%s_%s_%s.tar.gz", version.String(), i.goos, i.goarch)
	base := strings.TrimRight(i.downloadBase, "/") + "/" + release.Tag
	checksums, err := i.downloadBytes(ctx, base+"/checksums.txt", maxChecksumsBytes)
	if err != nil {
		return fmt.Errorf("download checksums: %w", err)
	}
	expected, err := checksumFor(checksums, archiveName)
	if err != nil {
		return err
	}

	dir := filepath.Dir(resolved)
	archive, err := os.CreateTemp(dir, ".agent-update-*.tar.gz")
	if err != nil {
		return fmt.Errorf("%w: create update file: %v", ErrManualUpdate, err)
	}
	archivePath := archive.Name()
	defer os.Remove(archivePath)

	actual, err := i.downloadFile(ctx, base+"/"+archiveName, archive, maxArchiveBytes)
	closeErr := archive.Close()
	if err != nil {
		return fmt.Errorf("download release archive: %w", err)
	}
	if closeErr != nil {
		return closeErr
	}
	if actual != expected {
		return fmt.Errorf("release checksum mismatch for %s", archiveName)
	}

	replacement, err := extractBinary(archivePath, dir, info.Mode().Perm())
	if err != nil {
		return err
	}
	defer os.Remove(replacement)
	if err := i.rename(replacement, resolved); err != nil {
		return fmt.Errorf("%w: replace executable: %v", ErrManualUpdate, err)
	}
	return nil
}

func (i Installer) downloadBytes(ctx context.Context, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "agent-cli-updater")
	resp, err := i.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s returned %s", url, resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("response exceeds %d bytes", limit)
	}
	return data, nil
}

func (i Installer) downloadFile(ctx context.Context, url string, dst io.Writer, limit int64) ([sha256.Size]byte, error) {
	var sum [sha256.Size]byte
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return sum, err
	}
	req.Header.Set("User-Agent", "agent-cli-updater")
	resp, err := i.client.Do(req)
	if err != nil {
		return sum, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return sum, fmt.Errorf("%s returned %s", url, resp.Status)
	}
	if resp.ContentLength > limit {
		return sum, fmt.Errorf("response exceeds %d bytes", limit)
	}
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(dst, hash), io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return sum, err
	}
	if n > limit {
		return sum, fmt.Errorf("response exceeds %d bytes", limit)
	}
	copy(sum[:], hash.Sum(nil))
	return sum, nil
}

func checksumFor(data []byte, filename string) ([sha256.Size]byte, error) {
	var zero [sha256.Size]byte
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || strings.TrimPrefix(fields[1], "*") != filename {
			continue
		}
		decoded, err := hex.DecodeString(fields[0])
		if err != nil || len(decoded) != sha256.Size {
			return zero, fmt.Errorf("invalid checksum for %s", filename)
		}
		var sum [sha256.Size]byte
		copy(sum[:], decoded)
		return sum, nil
	}
	return zero, fmt.Errorf("checksums.txt does not contain %s", filename)
}

func extractBinary(archivePath, dir string, mode os.FileMode) (string, error) {
	archive, err := os.Open(archivePath)
	if err != nil {
		return "", err
	}
	defer archive.Close()
	gz, err := gzip.NewReader(archive)
	if err != nil {
		return "", fmt.Errorf("open release archive: %w", err)
	}
	defer gz.Close()

	replacement, err := os.CreateTemp(dir, ".agent-update-*")
	if err != nil {
		return "", fmt.Errorf("%w: create replacement: %v", ErrManualUpdate, err)
	}
	replacementPath := replacement.Name()
	cleanup := true
	defer func() {
		replacement.Close()
		if cleanup {
			os.Remove(replacementPath)
		}
	}()

	found := false
	var unpacked int64
	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", fmt.Errorf("read release archive: %w", err)
		}
		clean := path.Clean(header.Name)
		if clean != header.Name || path.IsAbs(header.Name) || clean == ".." || strings.HasPrefix(clean, "../") {
			return "", fmt.Errorf("unsafe path %q in release archive", header.Name)
		}
		if header.Size < 0 || header.Size > maxUnpackedBytes-unpacked {
			return "", errors.New("release archive exceeds unpacked size limit")
		}
		unpacked += header.Size
		if clean != "agent" {
			continue
		}
		if found ||
			(header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA) ||
			header.Size > maxBinaryBytes {
			return "", errors.New("release archive contains an invalid agent binary")
		}
		n, err := io.Copy(replacement, io.LimitReader(tr, maxBinaryBytes+1))
		if err != nil || n != header.Size || n > maxBinaryBytes {
			return "", errors.New("release archive contains a truncated or oversized agent binary")
		}
		found = true
	}
	if !found {
		return "", errors.New("release archive does not contain agent")
	}
	if err := replacement.Chmod(mode); err != nil {
		return "", err
	}
	if err := replacement.Sync(); err != nil {
		return "", err
	}
	if err := replacement.Close(); err != nil {
		return "", err
	}
	cleanup = false
	return replacementPath, nil
}

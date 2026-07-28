package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallVerifiesAndAtomicallyReplacesExecutable(t *testing.T) {
	t.Parallel()
	const newBinary = "new verified binary"
	archive := testArchive(t, map[string]string{"README.md": "docs", "agent": newBinary})
	sum := sha256.Sum256(archive)
	installer, executable := testInstaller(t, archive, fmt.Sprintf("%x  agent-cli_0.2.0_darwin_arm64.tar.gz\n", sum))

	if err := installer.Install(context.Background(), testRelease()); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != newBinary {
		t.Fatalf("installed binary = %q, want %q", got, newBinary)
	}
	info, _ := os.Stat(executable)
	if info.Mode().Perm() != 0o751 {
		t.Fatalf("installed mode = %o, want 751", info.Mode().Perm())
	}
}

func TestInstallRejectsChecksumMismatchWithoutChangingExecutable(t *testing.T) {
	t.Parallel()
	archive := testArchive(t, map[string]string{"agent": "tampered"})
	installer, executable := testInstaller(t, archive, strings.Repeat("0", 64)+"  agent-cli_0.2.0_darwin_arm64.tar.gz\n")

	err := installer.Install(context.Background(), testRelease())
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("Install error = %v", err)
	}
	assertOldBinary(t, executable)
}

func TestInstallRejectsUnsafeArchivePath(t *testing.T) {
	t.Parallel()
	archive := testArchive(t, map[string]string{"../agent": "malicious"})
	sum := sha256.Sum256(archive)
	installer, executable := testInstaller(t, archive, fmt.Sprintf("%x  agent-cli_0.2.0_darwin_arm64.tar.gz\n", sum))

	err := installer.Install(context.Background(), testRelease())
	if err == nil || !strings.Contains(err.Error(), "unsafe path") {
		t.Fatalf("Install error = %v", err)
	}
	assertOldBinary(t, executable)
}

func TestInstallRenameFailurePreservesExecutable(t *testing.T) {
	t.Parallel()
	archive := testArchive(t, map[string]string{"agent": "new"})
	sum := sha256.Sum256(archive)
	installer, executable := testInstaller(t, archive, fmt.Sprintf("%x  agent-cli_0.2.0_darwin_arm64.tar.gz\n", sum))
	installer.rename = func(string, string) error { return fmt.Errorf("read-only install") }

	err := installer.Install(context.Background(), testRelease())
	if !errors.Is(err, ErrManualUpdate) {
		t.Fatalf("Install error = %v, want ErrManualUpdate", err)
	}
	assertOldBinary(t, executable)
}

func TestInstallRequiresManualUpdateOnWindows(t *testing.T) {
	t.Parallel()
	installer := NewInstaller()
	installer.goos = "windows"
	if err := installer.Install(context.Background(), testRelease()); !errors.Is(err, ErrManualUpdate) {
		t.Fatalf("Install error = %v, want ErrManualUpdate", err)
	}
}

func TestInstallBoundsDownloads(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	executable := filepath.Join(dir, "agent")
	if err := os.WriteFile(executable, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "checksums.txt") {
			fmt.Fprint(w, strings.Repeat("x", maxChecksumsBytes+1))
			return
		}
		t.Fatal("archive should not be requested after oversized checksums")
	}))
	defer server.Close()
	installer := Installer{
		client:         server.Client(),
		downloadBase:   server.URL,
		executablePath: executable,
		goos:           "darwin",
		goarch:         "arm64",
		rename:         os.Rename,
	}
	if err := installer.Install(context.Background(), testRelease()); err == nil ||
		!strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("Install error = %v", err)
	}
}

func TestChecksumForRejectsMalformedOrMissingEntry(t *testing.T) {
	t.Parallel()
	if _, err := checksumFor([]byte("xyz  file.tar.gz\n"), "file.tar.gz"); err == nil {
		t.Fatal("malformed checksum should fail")
	}
	if _, err := checksumFor([]byte(strings.Repeat("0", 64)+"  other.tar.gz\n"), "file.tar.gz"); err == nil {
		t.Fatal("missing checksum should fail")
	}
}

func testInstaller(t *testing.T, archive []byte, checksums string) (Installer, string) {
	t.Helper()
	dir := t.TempDir()
	executable := filepath.Join(dir, "agent")
	if err := os.WriteFile(executable, []byte("old binary"), 0o751); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/checksums.txt"):
			fmt.Fprint(w, checksums)
		case strings.HasSuffix(r.URL.Path, "/agent-cli_0.2.0_darwin_arm64.tar.gz"):
			w.Write(archive)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return Installer{
		client:         server.Client(),
		downloadBase:   server.URL,
		executablePath: executable,
		goos:           "darwin",
		goarch:         "arm64",
		rename:         os.Rename,
	}, executable
}

func testArchive(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, content := range files {
		if err := tw.WriteHeader(&tar.Header{
			Name: name,
			Mode: 0o755,
			Size: int64(len(content)),
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func testRelease() Release {
	return Release{
		Version:  "0.2.0",
		Tag:      "v0.2.0",
		NotesURL: "https://github.com/xautjzd/agent-cli/releases/tag/v0.2.0",
	}
}

func assertOldBinary(t *testing.T, path string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "old binary" {
		t.Fatalf("old binary changed to %q", got)
	}
}

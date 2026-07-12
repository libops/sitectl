package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAtomicCopyLocalPublishesCompleteFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	destination := filepath.Join(dir, "backup.sql.gz")
	if err := os.WriteFile(destination, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := atomicCopyLocal(strings.NewReader("complete-backup"), destination); err != nil {
		t.Fatalf("atomicCopyLocal() error = %v", err)
	}
	data, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "complete-backup" {
		t.Fatalf("destination = %q", data)
	}
	info, err := os.Stat(destination)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("destination mode = %o, want 600", got)
	}
	assertNoUploadTemps(t, dir)
}

func TestAtomicCopyLocalPreservesPriorFileOnCopyFailure(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	destination := filepath.Join(dir, "backup.sql.gz")
	if err := os.WriteFile(destination, []byte("known-good"), 0o600); err != nil {
		t.Fatal(err)
	}
	wantErr := errors.New("interrupted upload")
	err := atomicCopyLocal(&interruptedUploadReader{err: wantErr}, destination)
	if !errors.Is(err, wantErr) {
		t.Fatalf("atomicCopyLocal() error = %v, want %v", err, wantErr)
	}
	data, readErr := os.ReadFile(destination)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != "known-good" {
		t.Fatalf("prior destination changed to %q", data)
	}
	assertNoUploadTemps(t, dir)
}

func TestRemoteUploadTempPathStaysBesideDestination(t *testing.T) {
	t.Parallel()

	temp, err := remoteUploadTempPath("/srv/backups/site.sql.gz")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(temp, "/srv/backups/.site.sql.gz.sitectl-upload-") {
		t.Fatalf("remote temp path = %q", temp)
	}
}

type interruptedUploadReader struct {
	wrote bool
	err   error
}

func (r *interruptedUploadReader) Read(p []byte) (int, error) {
	if !r.wrote {
		r.wrote = true
		return copy(p, "partial"), nil
	}
	return 0, r.err
}

func assertNoUploadTemps(t *testing.T, dir string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, ".*.sitectl-upload-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary uploads remain: %v", matches)
	}
}

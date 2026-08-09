package cmd

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/libops/sitectl/pkg/config"
)

func TestValidateBackupTarTopologyAcceptsRegularFilesAndDirectories(t *testing.T) {
	t.Parallel()

	archive := backupTarBytes(t, []tar.Header{
		{Name: "./", Typeflag: tar.TypeDir, Mode: 0o755},
		{Name: "files/", Typeflag: tar.TypeDir, Mode: 0o750},
		{Name: "files/image.jpg", Typeflag: tar.TypeReg, Mode: 0o640, Size: 3},
	})
	if err := validateCompressedBackupTar(archive); err != nil {
		t.Fatalf("validateBackupTarTopology() error = %v", err)
	}
}

func TestValidateBackupTarTopologyRejectsUnsafeEntries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		header tar.Header
	}{
		{name: "absolute", header: tar.Header{Name: "/etc/passwd", Typeflag: tar.TypeReg}},
		{name: "parent", header: tar.Header{Name: "../../outside", Typeflag: tar.TypeReg}},
		{name: "normalized parent", header: tar.Header{Name: "files/../outside", Typeflag: tar.TypeReg}},
		{name: "backslash", header: tar.Header{Name: `files\outside`, Typeflag: tar.TypeReg}},
		{name: "symlink", header: tar.Header{Name: "files/link", Typeflag: tar.TypeSymlink, Linkname: "target"}},
		{name: "hardlink", header: tar.Header{Name: "files/link", Typeflag: tar.TypeLink, Linkname: "target"}},
		{name: "device", header: tar.Header{Name: "files/device", Typeflag: tar.TypeChar}},
		{name: "fifo", header: tar.Header{Name: "files/fifo", Typeflag: tar.TypeFifo}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if err := validateCompressedBackupTar(backupTarBytes(t, []tar.Header{tc.header})); err == nil {
				t.Fatalf("unsafe header %#v was accepted", tc.header)
			}
		})
	}
}

func TestValidateBackupTarTopologyRejectsUnsafeTopology(t *testing.T) {
	t.Parallel()

	archive := backupTarBytes(t, []tar.Header{
		{Name: "files", Typeflag: tar.TypeReg, Size: 0},
		{Name: "files/child", Typeflag: tar.TypeReg, Size: 0},
	})
	if err := validateCompressedBackupTar(archive); err == nil || !strings.Contains(err.Error(), "non-directory") {
		t.Fatalf("validateBackupTarTopology() error = %v, want unsafe topology", err)
	}
}

func TestValidateContextBackupArtifactRejectsDigestMismatch(t *testing.T) {
	t.Parallel()

	project := t.TempDir()
	backupDir := filepath.Join(project, "backups", "one")
	if err := os.MkdirAll(backupDir, 0o700); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(backupDir, "files.tar.gz")
	if err := os.WriteFile(archive, backupTarBytes(t, []tar.Header{{Name: "files/", Typeflag: tar.TypeDir}}), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := &config.Context{DockerHostType: config.ContextLocal, ProjectDir: project}
	if err := validateContextBackupArtifact(context.Background(), ctx, backupDir, "files.tar.gz", strings.Repeat("0", 64), true); err == nil || !strings.Contains(err.Error(), "SHA-256 mismatch") {
		t.Fatalf("validateContextBackupArtifact() error = %v, want digest mismatch", err)
	}
}

func TestValidateContextBackupArtifactAcceptsContainedChecksummedArchive(t *testing.T) {
	t.Parallel()

	project := t.TempDir()
	backupDir := filepath.Join(project, "backups", "one")
	if err := os.MkdirAll(backupDir, 0o700); err != nil {
		t.Fatal(err)
	}
	data := backupTarBytes(t, []tar.Header{{Name: "files/", Typeflag: tar.TypeDir}})
	archive := filepath.Join(backupDir, "files.tar.gz")
	if err := os.WriteFile(archive, data, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	ctx := &config.Context{DockerHostType: config.ContextLocal, ProjectDir: project}
	if err := validateContextBackupArtifact(context.Background(), ctx, backupDir, "files.tar.gz", hex.EncodeToString(digest[:]), true); err != nil {
		t.Fatalf("validateContextBackupArtifact() error = %v", err)
	}
}

func TestValidateContextBackupArtifactRejectsCorruptDatabaseGzip(t *testing.T) {
	t.Parallel()

	project := t.TempDir()
	backupDir := filepath.Join(project, "backups", "one")
	if err := os.MkdirAll(backupDir, 0o700); err != nil {
		t.Fatal(err)
	}
	data := []byte("not a gzip stream")
	archive := filepath.Join(backupDir, "mariadb.sql.gz")
	if err := os.WriteFile(archive, data, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	ctx := &config.Context{DockerHostType: config.ContextLocal, ProjectDir: project}
	if err := validateContextBackupArtifact(context.Background(), ctx, backupDir, "mariadb.sql.gz", hex.EncodeToString(digest[:]), false); err == nil || !strings.Contains(err.Error(), "open gzip") {
		t.Fatalf("validateContextBackupArtifact() error = %v, want corrupt database gzip refusal", err)
	}
}

func backupTarBytes(t *testing.T, headers []tar.Header) []byte {
	t.Helper()
	var output bytes.Buffer
	gzipWriter := gzip.NewWriter(&output)
	tarWriter := tar.NewWriter(gzipWriter)
	for index := range headers {
		header := headers[index]
		if err := tarWriter.WriteHeader(&header); err != nil {
			t.Fatalf("write tar header: %v", err)
		}
		if header.Size > 0 {
			if _, err := tarWriter.Write(bytes.Repeat([]byte("x"), int(header.Size))); err != nil {
				t.Fatalf("write tar payload: %v", err)
			}
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatalf("close tar writer: %v", err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatalf("close gzip writer: %v", err)
	}
	return output.Bytes()
}

func validateCompressedBackupTar(data []byte) error {
	gzipReader, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return err
	}
	defer gzipReader.Close()
	return validateBackupTarTopology(tar.NewReader(gzipReader))
}

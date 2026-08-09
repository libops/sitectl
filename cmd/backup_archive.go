package cmd

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/libops/sitectl/pkg/config"
	corejob "github.com/libops/sitectl/pkg/job"
)

// validateContextBackupArtifact is the checked-in parser used for restore
// admission. It verifies containment and integrity for every artifact and
// rejects tar topology that could escape or mutate a target unexpectedly.
func validateContextBackupArtifact(runCtx context.Context, ctx *config.Context, backupDir, archive, expectedChecksum string, tarArchive bool) error {
	if err := validateBackupArchiveName(archive); err != nil {
		return err
	}
	expectedChecksum = strings.ToLower(strings.TrimSpace(expectedChecksum))
	if !sha256HexPattern.MatchString(expectedChecksum) {
		return fmt.Errorf("expected SHA-256 checksum for %q is invalid", archive)
	}
	resolved, err := resolveContainedBackupFile(runCtx, ctx, backupDir, archive)
	if err != nil {
		return err
	}
	actualChecksum, err := contextFileSHA256(runCtx, ctx, resolved)
	if err != nil {
		return fmt.Errorf("calculate SHA-256 for %q: %w", archive, err)
	}
	if actualChecksum != expectedChecksum {
		return fmt.Errorf("SHA-256 mismatch for %q: got %s, want %s", archive, actualChecksum, expectedChecksum)
	}
	temporaryDir, err := os.MkdirTemp("", "sitectl-validate-backup-*")
	if err != nil {
		return fmt.Errorf("create local archive validation directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(temporaryDir) }()
	temporaryPath := filepath.Join(temporaryDir, "artifact.tar.gz")
	if err := corejob.DownloadContextFileContext(runCtx, ctx, resolved, temporaryPath); err != nil {
		return fmt.Errorf("download %q for safe topology validation: %w", archive, err)
	}
	file, err := os.Open(temporaryPath) // #nosec G304 -- this process created and populated temporaryPath.
	if err != nil {
		return err
	}
	defer file.Close()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return fmt.Errorf("checksum downloaded archive %q: %w", archive, err)
	}
	if downloadedChecksum := fmt.Sprintf("%x", hasher.Sum(nil)); downloadedChecksum != expectedChecksum {
		return fmt.Errorf("downloaded archive %q changed during validation: got SHA-256 %s, want %s", archive, downloadedChecksum, expectedChecksum)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("rewind downloaded archive %q: %w", archive, err)
	}
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return fmt.Errorf("open gzip archive %q: %w", archive, err)
	}
	defer gzipReader.Close()
	if !tarArchive {
		if _, err := io.Copy(io.Discard, gzipReader); err != nil {
			return fmt.Errorf("validate compressed database archive %q: %w", archive, err)
		}
		return nil
	}
	if err := validateBackupTarTopology(tar.NewReader(gzipReader)); err != nil {
		return fmt.Errorf("unsafe tar archive %q: %w", archive, err)
	}
	if _, err := io.Copy(io.Discard, gzipReader); err != nil {
		return fmt.Errorf("validate compressed archive %q: %w", archive, err)
	}
	return nil
}

func validateBackupTarTopology(reader *tar.Reader) error {
	entryTypes := map[string]byte{}
	hasDescendant := map[string]bool{}
	for {
		header, err := reader.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read tar header: %w", err)
		}
		name, err := safeBackupTarPath(header.Name)
		if err != nil {
			return err
		}
		if _, duplicate := entryTypes[name]; duplicate {
			return fmt.Errorf("duplicate archive entry %q", header.Name)
		}
		if header.Linkname != "" {
			return fmt.Errorf("archive entry %q declares a link target", header.Name)
		}
		if name == "." && header.Typeflag != tar.TypeDir {
			return fmt.Errorf("archive root entry %q is not a directory", header.Name)
		}
		switch header.Typeflag {
		case tar.TypeReg, tar.TypeRegA:
			if strings.HasSuffix(header.Name, "/") {
				return fmt.Errorf("regular file %q uses a directory path", header.Name)
			}
			if hasDescendant[name] {
				return fmt.Errorf("regular file %q replaces a directory ancestor", header.Name)
			}
		case tar.TypeDir:
		default:
			return fmt.Errorf("archive entry %q uses disallowed type %d (links, devices, and FIFOs are not restorable)", header.Name, header.Typeflag)
		}
		for parent := path.Dir(name); parent != "." && parent != "/"; parent = path.Dir(parent) {
			if existing, ok := entryTypes[parent]; ok && existing != tar.TypeDir {
				return fmt.Errorf("archive entry %q descends from non-directory %q", header.Name, parent)
			}
			hasDescendant[parent] = true
		}
		entryTypes[name] = header.Typeflag
	}
}

func safeBackupTarPath(value string) (string, error) {
	if value == "" || strings.ContainsAny(value, "\\\x00\r\n") || path.IsAbs(value) {
		return "", fmt.Errorf("archive entry path %q is unsafe", value)
	}
	for _, component := range strings.Split(value, "/") {
		if component == ".." {
			return "", fmt.Errorf("archive entry path %q contains a parent traversal", value)
		}
	}
	clean := path.Clean(value)
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("archive entry path %q escapes its target", value)
	}
	if clean == "." {
		return ".", nil
	}
	return clean, nil
}

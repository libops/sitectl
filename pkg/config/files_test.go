package config

import (
	"errors"
	"io/fs"
	"path/filepath"
	"testing"
)

func TestContextReadFileMissingIsErrNotExist(t *testing.T) {
	t.Parallel()

	ctx := &Context{DockerHostType: ContextLocal}
	_, err := ctx.ReadFile(filepath.Join(t.TempDir(), "missing.yml"))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("ReadFile() error = %v, want fs.ErrNotExist", err)
	}
}

func TestContextListFilesMissingIsErrNotExist(t *testing.T) {
	t.Parallel()

	ctx := &Context{DockerHostType: ContextLocal}
	_, err := ctx.ListFiles(filepath.Join(t.TempDir(), "missing"))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("ListFiles() error = %v, want fs.ErrNotExist", err)
	}
}

func TestContextReadSmallFileMissingIsErrNotExist(t *testing.T) {
	t.Parallel()

	ctx := &Context{DockerHostType: ContextLocal}
	_, err := ctx.ReadSmallFile(filepath.Join(t.TempDir(), "missing.env"))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("ReadSmallFile() error = %v, want fs.ErrNotExist", err)
	}
}

func TestNormalizeFileNotExistErrorMarksRemotePhrasing(t *testing.T) {
	t.Parallel()

	err := normalizeFileNotExistError(errors.New(`sftp: "no such file"`))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("normalizeFileNotExistError() = %v, want fs.ErrNotExist", err)
	}
}

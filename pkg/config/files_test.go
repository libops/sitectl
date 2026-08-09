package config

import (
	"errors"
	"io/fs"
	"path/filepath"
	"reflect"
	"strings"
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

func TestRemoteDirectoryPrefixesUsePOSIXPaths(t *testing.T) {
	t.Parallel()

	want := []string{"/srv", "/srv/customers", "/srv/customers/museum"}
	got := remoteDirectoryPrefixes(`/srv\customers//./museum`)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("remoteDirectoryPrefixes() = %v, want %v", got, want)
	}
	for _, prefix := range got {
		if strings.Contains(prefix, `\`) {
			t.Fatalf("remote directory prefix contains a client-platform separator: %q", prefix)
		}
	}
}

func TestRemoteRelativePathUsesPOSIXContainment(t *testing.T) {
	t.Parallel()

	got, err := remoteRelativePath(`/srv\customers\museum`, `/srv/customers/museum/config/settings.yml`)
	if err != nil {
		t.Fatalf("remoteRelativePath() error = %v", err)
	}
	if want := "config/settings.yml"; got != want {
		t.Fatalf("remoteRelativePath() = %q, want %q", got, want)
	}
	if _, err := remoteRelativePath("/srv/customers/museum", "/srv/customers/other/settings.yml"); err == nil {
		t.Fatal("remoteRelativePath() accepted a path outside the remote root")
	}
}

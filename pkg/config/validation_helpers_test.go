package config

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestComposeAccessCommandUsesDistinctArguments(t *testing.T) {
	ctx := &Context{
		DockerHostType: ContextLocal,
		ProjectDir:     "/srv/sites/museum",
		ComposeFile:    []string{"compose.yaml", "compose local.yaml"},
		EnvFile:        []string{".env"},
	}

	command := ctx.composeAccessCommand()
	wantArgs := []string{"docker", "compose", "-f", "compose.yaml", "-f", "compose local.yaml", "--env-file", ".env", "ps"}
	if !reflect.DeepEqual(command.Args, wantArgs) {
		t.Fatalf("composeAccessCommand().Args = %v, want %v", command.Args, wantArgs)
	}
	if command.Dir != ctx.ProjectDir {
		t.Fatalf("composeAccessCommand().Dir = %q, want %q", command.Dir, ctx.ProjectDir)
	}
}

func TestValidateProjectRegularFileRejectsLocalSymlinkEscapeAndDirectories(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.sh")
	if err := os.WriteFile(outside, []byte("echo unsafe\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "scripts"), 0o750); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(root, "scripts", "repair.sh")
	if err := os.Symlink(outside, symlink); err != nil {
		t.Skipf("create symlink: %v", err)
	}
	ctx := &Context{DockerHostType: ContextLocal, ProjectDir: root}
	if err := ctx.ValidateProjectRegularFile(root, symlink); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("ValidateProjectRegularFile(symlink) error = %v, want symlink refusal", err)
	}
	if err := ctx.ValidateProjectRegularFile(root, filepath.Join(root, "scripts")); err == nil || !strings.Contains(err.Error(), "not a regular") {
		t.Fatalf("ValidateProjectRegularFile(directory) error = %v, want regular-file refusal", err)
	}
}

func TestValidateRemoteProjectRegularFileRejectsSymlinkComponent(t *testing.T) {
	t.Parallel()

	inspector := fakeProjectFileInspector{
		infos: map[string]fs.FileInfo{
			"/srv/project/scripts":           fakeProjectFileInfo{name: "scripts", mode: fs.ModeDir | 0o750},
			"/srv/project/scripts/repair.sh": fakeProjectFileInfo{name: "repair.sh", mode: fs.ModeSymlink | 0o777},
		},
		realPaths: map[string]string{"/srv/project": "/srv/project"},
	}
	err := validateRemoteProjectRegularFile(inspector, "/srv/project", "/srv/project/scripts/repair.sh")
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("validateRemoteProjectRegularFile() error = %v, want symlink refusal", err)
	}
}

func TestWriteProjectFileRejectsLocalSymlinkParentAndTarget(t *testing.T) {
	t.Parallel()

	for _, target := range []string{"parent", "target"} {
		t.Run(target, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			outside := t.TempDir()
			if target == "parent" {
				if err := os.Symlink(outside, filepath.Join(root, "certs")); err != nil {
					t.Skipf("create parent symlink: %v", err)
				}
			} else {
				if err := os.Mkdir(filepath.Join(root, "certs"), 0o750); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(outside, "UID"), filepath.Join(root, "certs", "UID")); err != nil {
					t.Skipf("create target symlink: %v", err)
				}
			}
			ctx := &Context{DockerHostType: ContextLocal, ProjectDir: root}
			err := ctx.WriteProjectFile(root, filepath.Join(root, "certs", "UID"), []byte("1000\n"))
			if err == nil || !strings.Contains(err.Error(), "symlink") {
				t.Fatalf("WriteProjectFile() error = %v, want symlink refusal", err)
			}
			if _, err := os.Stat(filepath.Join(outside, "UID")); !os.IsNotExist(err) {
				t.Fatalf("outside UID changed through symlink: %v", err)
			}
		})
	}
}

func TestValidateRemoteProjectFileWriteRejectsSymlinkParent(t *testing.T) {
	t.Parallel()

	inspector := fakeProjectFileInspector{
		infos: map[string]fs.FileInfo{
			"/srv/project/certs": fakeProjectFileInfo{name: "certs", mode: fs.ModeSymlink | 0o777},
		},
		realPaths: map[string]string{"/srv/project": "/srv/project"},
	}
	err := validateRemoteProjectFileWrite(inspector, "/srv/project", "/srv/project/certs/UID")
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("validateRemoteProjectFileWrite() error = %v, want symlink refusal", err)
	}
}

type fakeProjectFileInspector struct {
	infos     map[string]fs.FileInfo
	realPaths map[string]string
}

func (f fakeProjectFileInspector) Lstat(name string) (os.FileInfo, error) {
	info, ok := f.infos[name]
	if !ok {
		return nil, fmt.Errorf("missing %s", name)
	}
	return info, nil
}

func (f fakeProjectFileInspector) RealPath(name string) (string, error) {
	resolved, ok := f.realPaths[name]
	if !ok {
		return "", fmt.Errorf("missing real path %s", name)
	}
	return resolved, nil
}

type fakeProjectFileInfo struct {
	name string
	mode fs.FileMode
}

func (f fakeProjectFileInfo) Name() string       { return f.name }
func (f fakeProjectFileInfo) Size() int64        { return 0 }
func (f fakeProjectFileInfo) Mode() fs.FileMode  { return f.mode }
func (f fakeProjectFileInfo) ModTime() time.Time { return time.Time{} }
func (f fakeProjectFileInfo) IsDir() bool        { return f.mode.IsDir() }
func (f fakeProjectFileInfo) Sys() any           { return nil }

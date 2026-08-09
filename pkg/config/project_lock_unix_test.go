//go:build !windows

package config

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalProjectMutationLockIdentitySurvivesDirectoryRename(t *testing.T) {
	root := t.TempDir()
	beforePath := filepath.Join(root, "before")
	afterPath := filepath.Join(root, "after")
	if err := os.Mkdir(beforePath, 0o750); err != nil {
		t.Fatal(err)
	}
	beforeIdentity, err := localProjectMutationLockIdentity(beforePath)
	if err != nil {
		t.Fatalf("localProjectMutationLockIdentity(before) error = %v", err)
	}
	if !strings.HasPrefix(beforeIdentity, "unix:") {
		t.Fatalf("local project identity = %q, want Unix device/inode identity", beforeIdentity)
	}
	if err := os.Rename(beforePath, afterPath); err != nil {
		t.Fatal(err)
	}
	afterIdentity, err := localProjectMutationLockIdentity(afterPath)
	if err != nil {
		t.Fatalf("localProjectMutationLockIdentity(after) error = %v", err)
	}
	if afterIdentity != beforeIdentity {
		t.Fatalf("same directory identities differ after rename: before=%q after=%q", beforeIdentity, afterIdentity)
	}
	if projectMutationLockFilename(afterIdentity) != projectMutationLockFilename(beforeIdentity) {
		t.Fatal("same local filesystem object produced different project lock paths")
	}
}

func TestLocalProjectMutationLockUsesPrivatePerUserDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	lockPath, err := localProjectMutationLockPath("unix:1:2")
	if err != nil {
		t.Fatalf("localProjectMutationLockPath() error = %v", err)
	}
	wantDirectory := filepath.Join(home, ".sitectl", "locks")
	if filepath.Dir(lockPath) != wantDirectory {
		t.Fatalf("local project lock directory = %q, want %q", filepath.Dir(lockPath), wantDirectory)
	}
	info, err := os.Lstat(wantDirectory)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatalf("local project lock directory mode = %v, want owner-only directory", info.Mode())
	}
}

func TestLocalProjectMutationLockDirectoryOwnerAllowed(t *testing.T) {
	tests := []struct {
		name    string
		owner   uint32
		uid     uint32
		private bool
		want    bool
	}{
		{name: "operator-owned home", owner: 1000, uid: 1000, want: true},
		{name: "root-owned home", owner: 0, uid: 1000, want: true},
		{name: "root-owned private state", owner: 0, uid: 1000, private: true, want: false},
		{name: "other-user-owned home", owner: 1001, uid: 1000, want: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := localProjectMutationLockDirectoryOwnerAllowed(test.owner, test.uid, test.private); got != test.want {
				t.Fatalf("localProjectMutationLockDirectoryOwnerAllowed(%d, %d, %t) = %t, want %t", test.owner, test.uid, test.private, got, test.want)
			}
		})
	}
}

func TestAcquireLocalProjectMutationLockRejectsSymlink(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	lockPath, err := localProjectMutationLockPath("unix:3:4")
	if err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(home, "victim")
	if err := os.WriteFile(victim, []byte("do not lock\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, lockPath); err != nil {
		t.Fatal(err)
	}
	if _, err := acquireLocalProjectMutationLock(context.Background(), context.Background(), lockPath); err == nil {
		t.Fatal("symlink project lock was accepted")
	}
	data, err := os.ReadFile(victim)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "do not lock\n" {
		t.Fatalf("symlink target changed: %q", data)
	}
}

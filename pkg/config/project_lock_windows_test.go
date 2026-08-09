//go:build windows

package config

import (
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
	if !strings.HasPrefix(beforeIdentity, "windows:") {
		t.Fatalf("local project identity = %q, want Windows volume/file identity", beforeIdentity)
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

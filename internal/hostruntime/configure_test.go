package hostruntime

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSecureSSHAccessRepairsOwnershipModes(t *testing.T) {
	home := t.TempDir()
	directory := filepath.Join(home, ".ssh")
	if err := os.Mkdir(directory, 0o777); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "authorized_keys")
	if err := os.WriteFile(path, []byte("ssh-ed25519 test\n"), 0o666); err != nil {
		t.Fatal(err)
	}
	if err := secureSSHAccess(home, os.Getuid(), os.Getgid()); err != nil {
		t.Fatalf("secureSSHAccess() error = %v", err)
	}
	for path, want := range map[string]os.FileMode{directory: 0o700, path: 0o600} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != want {
			t.Fatalf("%s mode = %o, want %o", path, info.Mode().Perm(), want)
		}
	}
}

func TestSecureSSHAccessRejectsAuthorizedKeysSymlink(t *testing.T) {
	home := t.TempDir()
	directory := filepath.Join(home, ".ssh")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(home, "target")
	if err := os.WriteFile(target, []byte("ssh-ed25519 test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(directory, "authorized_keys")); err != nil {
		t.Fatal(err)
	}
	if err := secureSSHAccess(home, os.Getuid(), os.Getgid()); err == nil {
		t.Fatal("expected authorized_keys symlink to fail")
	}
}

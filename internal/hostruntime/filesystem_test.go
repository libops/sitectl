package hostruntime

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConvergeFilesystemForIDsSecuresProjectAndEnvironment(t *testing.T) {
	project := filepath.Join(t.TempDir(), "app")
	if err := os.Mkdir(project, 0o777); err != nil {
		t.Fatal(err)
	}
	environment := filepath.Join(project, ".env")
	if err := os.WriteFile(environment, []byte("VALUE=one\n"), 0o666); err != nil {
		t.Fatal(err)
	}
	if err := convergeFilesystemForIDs(project, os.Geteuid(), os.Getegid()); err != nil {
		t.Fatalf("convergeFilesystemForIDs() error = %v", err)
	}
	if info, _ := os.Stat(project); info.Mode().Perm() != 0o775 {
		t.Fatalf("project mode = %o", info.Mode().Perm())
	}
	if info, _ := os.Stat(environment); info.Mode().Perm() != 0o640 {
		t.Fatalf("environment mode = %o", info.Mode().Perm())
	}
}

func TestConvergeFilesystemForIDsRejectsLinkedEnvironment(t *testing.T) {
	project := filepath.Join(t.TempDir(), "app")
	if err := os.Mkdir(project, 0o775); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chmod(project, 0o775); err != nil {
			t.Errorf("restore project mode: %v", err)
		}
	}()
	environment := filepath.Join(project, ".env")
	if err := os.WriteFile(environment, []byte("VALUE=one\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(environment, filepath.Join(project, "alias")); err != nil {
		t.Fatal(err)
	}
	if err := convergeFilesystemForIDs(project, os.Geteuid(), os.Getegid()); err == nil {
		t.Fatal("expected hard-linked environment to fail")
	}
}

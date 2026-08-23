package hostruntime

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSecureRuntimeHomeRejectsReplacementSymlink(t *testing.T) {
	home := buildTestRuntimeHome(t)
	path := filepath.Join(home, "run.sh")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("profile.sh", path); err != nil {
		t.Fatal(err)
	}
	err := SecureRuntimeHome(SecureRuntimeOptions{Home: home, TrustedUID: uint32(os.Geteuid()), TrustedGID: uint32(os.Getegid()), RuntimeGID: uint32(os.Getegid())})
	if err == nil {
		t.Fatal("expected replacement symlink to fail")
	}
}

func TestSecureRuntimeHomeNormalizesReviewedFiles(t *testing.T) {
	home := buildTestRuntimeHome(t)
	options := SecureRuntimeOptions{Home: home, TrustedUID: uint32(os.Geteuid()), TrustedGID: uint32(os.Getegid()), RuntimeGID: uint32(os.Getegid())}
	if err := SecureRuntimeHome(options); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"run.sh", "init"} {
		info, err := os.Stat(filepath.Join(home, name))
		if err != nil || info.Mode().Perm() != 0o755 {
			t.Fatalf("%s mode = %v, %v", name, info.Mode().Perm(), err)
		}
	}
}

func buildTestRuntimeHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	for _, name := range []string{"run.sh", "profile.sh", "host-conf.sh", "lifecycle-entrypoint.sh", "deploy-rollout.sh", "init", "up", "down", "rollout"} {
		if err := os.WriteFile(filepath.Join(home, name), []byte("#!/bin/sh\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return home
}

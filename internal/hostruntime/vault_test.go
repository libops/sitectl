package hostruntime

import (
	"os"
	"path/filepath"
	"testing"
)

func TestVaultReadinessRejectsTokenOutsideSafeDirectory(t *testing.T) {
	root := t.TempDir()
	options := vaultReadinessDefaults(VaultReadinessOptions{
		SafeDir:     filepath.Join(root, "vault"),
		TokenPath:   filepath.Join(root, "other", "token"),
		ReadyMarker: filepath.Join(root, "run", "ready"),
	})
	if err := validateVaultReadiness(options); err == nil {
		t.Fatal("expected token path to fail")
	}
}

func TestPrepareVaultReadinessRejectsSymlinkToken(t *testing.T) {
	root := t.TempDir()
	safe := filepath.Join(root, "vault")
	if err := os.Mkdir(safe, 0o700); err != nil {
		t.Fatal(err)
	}
	token := filepath.Join(safe, "token")
	if err := os.Symlink(filepath.Join(root, "outside"), token); err != nil {
		t.Fatal(err)
	}
	err := PrepareVaultReadiness(VaultReadinessOptions{
		SafeDir:     safe,
		TokenPath:   token,
		ReadyMarker: filepath.Join(root, "ready"),
	})
	if err == nil {
		t.Fatal("expected symlink token to fail")
	}
}

func TestVaultTokenReadyRequiresContentAndOneLink(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if vaultTokenReady(path) {
		t.Fatal("empty token reported ready")
	}
	if err := os.WriteFile(path, []byte("token"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !vaultTokenReady(path) {
		t.Fatal("non-empty regular token not ready")
	}
}

package hostruntime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"syscall"
	"time"
)

var vaultTokenNamePattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// VaultReadinessOptions controls the dedicated Vault Agent sink and readiness marker.
type VaultReadinessOptions struct {
	TokenPath   string
	SafeDir     string
	ReadyMarker string
	Timeout     time.Duration
}

// PrepareVaultReadiness removes stale state and creates the root-only sink directory.
func PrepareVaultReadiness(options VaultReadinessOptions) error {
	options = vaultReadinessDefaults(options)
	if err := validateVaultReadiness(options); err != nil {
		return err
	}
	if err := removeRegular(options.ReadyMarker); err != nil {
		return err
	}
	if err := removeRegular(options.TokenPath); err != nil {
		return err
	}
	return secureDirectory(options.SafeDir, 0o700, 0, 0)
}

// WaitForVaultReadiness waits for a non-empty, single-link regular sink token.
func WaitForVaultReadiness(ctx context.Context, options VaultReadinessOptions) error {
	options = vaultReadinessDefaults(options)
	if err := validateVaultReadiness(options); err != nil {
		return err
	}
	if options.Timeout < time.Second || options.Timeout > 5*time.Minute {
		return fmt.Errorf("vault agent readiness timeout must be between one second and five minutes")
	}
	deadline := time.NewTimer(options.Timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if vaultTokenReady(options.TokenPath) {
			if err := mkdirAllNoSymlink(filepath.Dir(options.ReadyMarker), 0o755); err != nil {
				return err
			}
			return writeAtomic(options.ReadyMarker, nil, 0o644)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("vault agent did not publish a token within %s", options.Timeout)
		case <-ticker.C:
		}
	}
}

// ClearVaultReadiness removes the current-boot readiness marker.
func ClearVaultReadiness(options VaultReadinessOptions) error {
	options = vaultReadinessDefaults(options)
	if err := validateVaultReadiness(options); err != nil {
		return err
	}
	return removeRegular(options.ReadyMarker)
}

func vaultReadinessDefaults(options VaultReadinessOptions) VaultReadinessOptions {
	if options.SafeDir == "" {
		options.SafeDir = "/mnt/disks/data/vault"
	}
	if options.TokenPath == "" {
		options.TokenPath = filepath.Join(options.SafeDir, "token")
	}
	if options.ReadyMarker == "" {
		options.ReadyMarker = "/run/cloud-compose/vault-agent.ready"
	}
	if options.Timeout == 0 {
		options.Timeout = time.Minute
	}
	return options
}

func validateVaultReadiness(options VaultReadinessOptions) error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("vault agent readiness must run as root")
	}
	for label, path := range map[string]string{"safe directory": options.SafeDir, "token": options.TokenPath, "ready marker": options.ReadyMarker} {
		if !safeAbsolutePath(path) {
			return fmt.Errorf("unsafe Vault Agent %s path: %s", label, path)
		}
	}
	name := filepath.Base(options.TokenPath)
	if filepath.Dir(options.TokenPath) != options.SafeDir || name == "." || name == ".." || !vaultTokenNamePattern.MatchString(name) {
		return fmt.Errorf("vault agent token must be one file directly inside %s", options.SafeDir)
	}
	parent := filepath.Dir(options.SafeDir)
	resolved, err := filepath.EvalSymlinks(parent)
	if err != nil || resolved != parent {
		return fmt.Errorf("vault agent safe-directory parent is missing or unsafe: %s", parent)
	}
	for _, path := range []string{options.SafeDir, options.TokenPath} {
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("unsafe Vault Agent path: %s", path)
		}
		if path == options.SafeDir && !info.IsDir() || path == options.TokenPath && !info.Mode().IsRegular() {
			return fmt.Errorf("unsafe Vault Agent path type: %s", path)
		}
	}
	return nil
}

func vaultTokenReady(path string) bool {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() == 0 {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Nlink == 1
}

package hostruntime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// VaultAgentOptions controls the explicitly enabled Vault Agent service.
type VaultAgentOptions struct {
	Enabled     string
	Config      string
	Binary      string
	ReadyMarker string
	Readiness   VaultReadinessOptions
	Stdout      io.Writer
	Stderr      io.Writer
}

// InitializeVaultAgent enables the service only after its trusted inputs are ready.
func InitializeVaultAgent(ctx context.Context, options VaultAgentOptions) error {
	options = vaultAgentDefaults(options)
	enabled, err := vaultAgentEnabled(options.Enabled)
	if err != nil {
		return err
	}
	if !enabled {
		disableVaultAgent(options)
		return removeRegular(options.ReadyMarker)
	}
	initialized := false
	defer func() {
		if !initialized {
			disableVaultAgent(options)
		}
	}()
	if err := requireSingleRegular(options.Config, false); err != nil {
		return fmt.Errorf("vault agent is enabled but its config is missing or unsafe")
	}
	if err := requireSingleRegular(options.Binary, true); err != nil {
		return fmt.Errorf("vault agent is enabled but the Vault binary is missing or unsafe")
	}
	if err := PrepareVaultReadiness(options.Readiness); err != nil {
		return err
	}
	if err := runVaultSystemctl(ctx, options, "daemon-reload"); err != nil {
		return err
	}
	if err := runVaultSystemctl(ctx, options, "enable", "cloud-compose-vault-agent.service"); err != nil {
		return err
	}
	if err := runVaultSystemctl(ctx, options, "restart", "cloud-compose-vault-agent.service"); err != nil {
		return err
	}
	if err := AssertVaultAgentReady(ctx, options); err != nil {
		return err
	}
	initialized = true
	return nil
}

// AssertVaultAgentReady gates applications on the explicitly enabled service.
func AssertVaultAgentReady(ctx context.Context, options VaultAgentOptions) error {
	options = vaultAgentDefaults(options)
	enabled, err := vaultAgentEnabled(options.Enabled)
	if err != nil || !enabled {
		return err
	}
	if err := requireSingleRegular(options.ReadyMarker, false); err != nil {
		return fmt.Errorf("vault agent is enabled but has not published readiness")
	}
	command := exec.CommandContext(ctx, "systemctl", "is-active", "--quiet", "cloud-compose-vault-agent.service")
	if err := command.Run(); err != nil {
		return fmt.Errorf("vault agent is enabled but its service is not active")
	}
	return nil
}

func vaultAgentDefaults(options VaultAgentOptions) VaultAgentOptions {
	if options.Enabled == "" {
		options.Enabled = "false"
	}
	if options.Config == "" {
		options.Config = "/etc/vault-agent.d/cloud-compose.hcl"
	}
	if options.Binary == "" {
		options.Binary = "/usr/local/bin/vault"
	}
	if options.ReadyMarker == "" {
		options.ReadyMarker = "/run/cloud-compose/vault-agent.ready"
	}
	if options.Readiness.ReadyMarker == "" {
		options.Readiness.ReadyMarker = options.ReadyMarker
	}
	if options.Stdout == nil {
		options.Stdout = io.Discard
	}
	if options.Stderr == nil {
		options.Stderr = os.Stderr
	}
	return options
}

func vaultAgentEnabled(value string) (bool, error) {
	switch value {
	case "false":
		return false, nil
	case "true":
		return true, nil
	default:
		return false, fmt.Errorf("VAULT_AGENT_ENABLED must be true or false")
	}
}

func requireSingleRegular(path string, executable bool) error {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || executable && info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("unsafe regular file: %s", path)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Nlink != 1 {
		return fmt.Errorf("unsafe regular file: %s", path)
	}
	return nil
}

func runVaultSystemctl(ctx context.Context, options VaultAgentOptions, args ...string) error {
	command := exec.CommandContext(ctx, "systemctl", args...)
	command.Stdout, command.Stderr = options.Stdout, options.Stderr
	if err := command.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return fmt.Errorf("systemctl failed with status %d", exit.ExitCode())
		}
		return err
	}
	return nil
}

func disableVaultAgent(options VaultAgentOptions) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = runVaultSystemctl(ctx, options, "disable", "--now", "cloud-compose-vault-agent.service")
}

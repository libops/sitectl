package hostruntime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// MetadataFirewallOptions controls GCP metadata isolation for the host and containers.
type MetadataFirewallOptions struct {
	Provider string
	Mode     string
	Stdout   io.Writer
	Stderr   io.Writer
}

type firewallRule struct {
	table string
	chain string
	args  []string
}

// ConfigureMetadataFirewall installs the idempotent GCP metadata deny policy.
func ConfigureMetadataFirewall(ctx context.Context, options MetadataFirewallOptions) error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("metadata firewall configuration must run as root")
	}
	if options.Provider == "" {
		return fmt.Errorf("CLOUD_COMPOSE_PROVIDER is required before configuring metadata isolation")
	}
	if options.Provider != "gcp" {
		return nil
	}
	if options.Mode == "" {
		options.Mode = "full"
	}
	if options.Mode != "full" && options.Mode != "pre-docker" {
		return fmt.Errorf("unknown metadata firewall mode %q", options.Mode)
	}
	if options.Stdout == nil {
		options.Stdout = io.Discard
	}
	if options.Stderr == nil {
		options.Stderr = os.Stderr
	}
	iptables, err := exec.LookPath("iptables")
	if err != nil {
		return fmt.Errorf("iptables is required to isolate the GCP metadata service")
	}
	for _, rule := range metadataFirewallRules(options.Mode) {
		if err := ensureFirewallRule(ctx, iptables, rule, options); err != nil {
			return err
		}
	}
	return nil
}

func metadataFirewallRules(mode string) []firewallRule {
	address := "169.254.169.254/32"
	rules := []firewallRule{
		{table: "mangle", chain: "PREROUTING", args: []string{"-d", address, "-j", "DROP"}},
		{table: "mangle", chain: "PREROUTING", args: []string{"-d", address, "-p", "tcp", "--dport", "53", "-j", "ACCEPT"}},
		{table: "mangle", chain: "PREROUTING", args: []string{"-d", address, "-p", "udp", "--dport", "53", "-j", "ACCEPT"}},
		{table: "filter", chain: "OUTPUT", args: []string{"-m", "owner", "!", "--uid-owner", "0", "-d", address, "-p", "tcp", "--dport", "80", "-j", "DROP"}},
		{table: "filter", chain: "OUTPUT", args: []string{"-m", "owner", "!", "--uid-owner", "0", "-d", address, "-p", "tcp", "--dport", "443", "-j", "DROP"}},
	}
	if mode == "full" {
		rules = append(rules,
			firewallRule{table: "filter", chain: "DOCKER-USER", args: []string{"-d", address, "-j", "DROP"}},
			firewallRule{table: "filter", chain: "DOCKER-USER", args: []string{"-d", address, "-p", "tcp", "--dport", "53", "-j", "ACCEPT"}},
			firewallRule{table: "filter", chain: "DOCKER-USER", args: []string{"-d", address, "-p", "udp", "--dport", "53", "-j", "ACCEPT"}},
			firewallRule{table: "filter", chain: "FORWARD", args: []string{"-d", address, "-j", "DROP"}},
			firewallRule{table: "filter", chain: "FORWARD", args: []string{"-d", address, "-p", "tcp", "--dport", "53", "-j", "ACCEPT"}},
			firewallRule{table: "filter", chain: "FORWARD", args: []string{"-d", address, "-p", "udp", "--dport", "53", "-j", "ACCEPT"}},
		)
	}
	return rules
}

func ensureFirewallRule(ctx context.Context, iptables string, rule firewallRule, options MetadataFirewallOptions) error {
	check := append([]string{"-t", rule.table, "-C", rule.chain}, rule.args...)
	command := exec.CommandContext(ctx, iptables, check...)
	command.Stdout, command.Stderr = options.Stdout, io.Discard
	err := command.Run()
	if err == nil {
		return nil
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 {
		return fmt.Errorf("iptables %s: %w", strings.Join(check, " "), err)
	}
	insert := append([]string{"-t", rule.table, "-I", rule.chain, "1"}, rule.args...)
	command = exec.CommandContext(ctx, iptables, insert...)
	command.Stdout, command.Stderr = options.Stdout, options.Stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("iptables %s: %w", strings.Join(insert, " "), err)
	}
	return nil
}

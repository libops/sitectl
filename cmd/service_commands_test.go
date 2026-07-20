package cmd

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/libops/sitectl/pkg/docker"
	"github.com/spf13/cobra"
)

func TestCoreServiceCommandsExposeExpectedSubcommands(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		cmd         *cobra.Command
		subcommands []string
	}{
		{name: "mariadb", cmd: mariaDBCommand(), subcommands: []string{"backup", "restore", "status", "sync", "upgrade"}},
		{name: "traefik", cmd: traefikCommand(), subcommands: []string{"status"}},
		{name: "solr", cmd: solrCommand(), subcommands: []string{"info", "status"}},
		{name: "valkey", cmd: valkeyCommand(), subcommands: []string{"ping", "status"}},
		{name: "memcached", cmd: memcachedCommand(), subcommands: []string{"stats", "status"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := tc.cmd.Name(); got != tc.name {
				t.Fatalf("command name = %q, want %q", got, tc.name)
			}
			var got []string
			for _, child := range tc.cmd.Commands() {
				got = append(got, child.Name())
			}
			if !reflect.DeepEqual(got, tc.subcommands) {
				t.Fatalf("%s subcommands = %v, want %v", tc.name, got, tc.subcommands)
			}
		})
	}
}

func TestServiceStatusCommandUsesDefaultServiceFlag(t *testing.T) {
	t.Parallel()

	cmd := serviceStatusCommand("solr")
	flag := cmd.Flags().Lookup("service")
	if flag == nil {
		t.Fatal("expected --service flag")
	}
	if flag.DefValue != "solr" {
		t.Fatalf("--service default = %q, want solr", flag.DefValue)
	}
}

func TestResolveServiceContainerRejectsEmptyService(t *testing.T) {
	t.Parallel()

	_, err := resolveServiceContainer(&cobra.Command{Use: "status"}, " \t\n ")
	if err == nil {
		t.Fatal("expected empty service name to fail")
	}
	if !strings.Contains(err.Error(), "service name cannot be empty") {
		t.Fatalf("resolveServiceContainer() error = %v", err)
	}
}

func TestResolveContainerExecutablePrefersInstalledBinary(t *testing.T) {
	previous := serviceExecCapture
	t.Cleanup(func() {
		serviceExecCapture = previous
	})

	var gotCmd []string
	serviceExecCapture = func(ctx context.Context, cli *docker.DockerClient, container, workingDir string, cmd []string) (string, error) {
		gotCmd = append([]string{}, cmd...)
		if container != "valkey-1" {
			t.Fatalf("container = %q, want valkey-1", container)
		}
		return "/usr/bin/valkey-cli", nil
	}

	got := resolveContainerExecutable(&cobra.Command{Use: "ping"}, nil, "valkey-1", "valkey-cli", "redis-cli")
	if got != "valkey-cli" {
		t.Fatalf("resolveContainerExecutable() = %q, want valkey-cli", got)
	}
	wantCmd := []string{"sh", "-lc", "command -v valkey-cli"}
	if !reflect.DeepEqual(gotCmd, wantCmd) {
		t.Fatalf("exec command = %v, want %v", gotCmd, wantCmd)
	}
}

func TestResolveContainerExecutableFallsBackWhenPreferredMissing(t *testing.T) {
	previous := serviceExecCapture
	t.Cleanup(func() {
		serviceExecCapture = previous
	})

	serviceExecCapture = func(ctx context.Context, cli *docker.DockerClient, container, workingDir string, cmd []string) (string, error) {
		return "", fmt.Errorf("not found")
	}

	got := resolveContainerExecutable(&cobra.Command{Use: "ping"}, nil, "valkey-1", "valkey-cli", "redis-cli")
	if got != "redis-cli" {
		t.Fatalf("resolveContainerExecutable() = %q, want redis-cli", got)
	}
}

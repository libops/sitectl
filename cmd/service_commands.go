package cmd

import (
	"fmt"
	"strings"

	"github.com/libops/sitectl/pkg/config"
	"github.com/libops/sitectl/pkg/docker"
	"github.com/spf13/cobra"
)

func registerCoreServiceCommands() {
	RootCmd.AddCommand(
		mariaDBCommand(),
		traefikCommand(),
		solrCommand(),
		valkeyCommand(),
		memcachedCommand(),
	)
}

func rootHasCommandName(name string) bool {
	for _, command := range RootCmd.Commands() {
		if command.Name() == name {
			return true
		}
	}
	return false
}

type serviceContainer struct {
	context       *config.Context
	cli           *docker.DockerClient
	containerName string
	service       string
}

var serviceExecCapture = docker.ExecCapture

func resolveServiceContainer(cmd *cobra.Command, service string) (*serviceContainer, error) {
	service = strings.TrimSpace(service)
	if service == "" {
		return nil, fmt.Errorf("service name cannot be empty")
	}
	ctx, err := resolveCurrentContext(cmd)
	if err != nil {
		return nil, err
	}
	cli, err := docker.GetDockerCli(ctx)
	if err != nil {
		return nil, err
	}
	containerName, err := cli.GetContainerNameContext(cmd.Context(), ctx, service)
	if err != nil {
		_ = cli.Close()
		return nil, fmt.Errorf("find %s container: %w", service, err)
	}
	if strings.TrimSpace(containerName) == "" {
		_ = cli.Close()
		return nil, fmt.Errorf("unable to find %s container for context %q", service, ctx.Name)
	}
	return &serviceContainer{
		context:       ctx,
		cli:           cli,
		containerName: containerName,
		service:       service,
	}, nil
}

func serviceStatusCommand(defaultService string) *cobra.Command {
	opts := struct {
		service string
	}{service: defaultService}
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show the compose service container status",
		RunE: func(cmd *cobra.Command, args []string) error {
			target, err := resolveServiceContainer(cmd, opts.service)
			if err != nil {
				return err
			}
			defer target.cli.Close()

			inspect, err := target.cli.CLI.ContainerInspect(cmd.Context(), target.containerName)
			if err != nil {
				return fmt.Errorf("inspect %s container: %w", target.service, err)
			}

			status := "unknown"
			health := ""
			if inspect.State != nil {
				status = inspect.State.Status
				if inspect.State.Health != nil {
					health = inspect.State.Health.Status
				}
			}
			if health == "" {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s: %s (%s)\n", target.service, status, strings.TrimPrefix(target.containerName, "/"))
				return nil
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s: %s, health=%s (%s)\n", target.service, status, health, strings.TrimPrefix(target.containerName, "/"))
			return nil
		},
	}
	cmd.Flags().StringVar(&opts.service, "service", defaultService, "Compose service name")
	return cmd
}

func solrCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "solr",
		Short:   "Operate on the Solr service in the active context",
		GroupID: "ops",
	}
	cmd.AddCommand(
		serviceStatusCommand("solr"),
		solrInfoCommand(),
	)
	return cmd
}

func solrInfoCommand() *cobra.Command {
	opts := struct {
		service string
	}{service: "solr"}
	cmd := &cobra.Command{
		Use:   "info",
		Short: "Run Solr's built-in status command",
		RunE: func(cmd *cobra.Command, args []string) error {
			target, err := resolveServiceContainer(cmd, opts.service)
			if err != nil {
				return err
			}
			defer target.cli.Close()

			output, err := docker.ExecCapture(cmd.Context(), target.cli, target.containerName, "", []string{"solr", "status"})
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), output)
			return nil
		},
	}
	cmd.Flags().StringVar(&opts.service, "service", "solr", "Compose service name")
	return cmd
}

func valkeyCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "valkey",
		Short:   "Operate on the Valkey service in the active context",
		GroupID: "ops",
	}
	cmd.AddCommand(
		serviceStatusCommand("valkey"),
		valkeyPingCommand(),
	)
	return cmd
}

func valkeyPingCommand() *cobra.Command {
	opts := struct {
		service string
	}{service: "valkey"}
	cmd := &cobra.Command{
		Use:   "ping",
		Short: "Ping Valkey from inside its container",
		RunE: func(cmd *cobra.Command, args []string) error {
			target, err := resolveServiceContainer(cmd, opts.service)
			if err != nil {
				return err
			}
			defer target.cli.Close()

			client := resolveContainerExecutable(cmd, target.cli, target.containerName, "valkey-cli", "redis-cli")
			output, err := docker.ExecCapture(cmd.Context(), target.cli, target.containerName, "", []string{client, "ping"})
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), output)
			return nil
		},
	}
	cmd.Flags().StringVar(&opts.service, "service", "valkey", "Compose service name")
	return cmd
}

func memcachedCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "memcached",
		Short:   "Operate on the Memcached service in the active context",
		GroupID: "ops",
	}
	cmd.AddCommand(
		serviceStatusCommand("memcached"),
		memcachedStatsCommand(),
	)
	return cmd
}

func memcachedStatsCommand() *cobra.Command {
	opts := struct {
		service string
	}{service: "memcached"}
	cmd := &cobra.Command{
		Use:   "stats",
		Short: "Show Memcached stats from inside its container",
		RunE: func(cmd *cobra.Command, args []string) error {
			target, err := resolveServiceContainer(cmd, opts.service)
			if err != nil {
				return err
			}
			defer target.cli.Close()

			output, err := docker.ExecCapture(cmd.Context(), target.cli, target.containerName, "", []string{
				"sh",
				"-lc",
				"if command -v memcached-tool >/dev/null 2>&1; then memcached-tool 127.0.0.1:11211 stats; else printf 'stats\\r\\nquit\\r\\n' | nc 127.0.0.1 11211; fi",
			})
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), output)
			return nil
		},
	}
	cmd.Flags().StringVar(&opts.service, "service", "memcached", "Compose service name")
	return cmd
}

func resolveContainerExecutable(cmd *cobra.Command, cli *docker.DockerClient, containerName string, preferred, fallback string) string {
	if _, err := serviceExecCapture(cmd.Context(), cli, containerName, "", []string{"sh", "-lc", "command -v " + preferred}); err == nil {
		return preferred
	}
	return fallback
}

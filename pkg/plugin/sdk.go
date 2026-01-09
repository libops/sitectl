package plugin

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/libops/sitectl/pkg/config"
	"github.com/libops/sitectl/pkg/docker"
	"github.com/spf13/cobra"
)

// Metadata contains information about a plugin
type Metadata struct {
	Name        string
	Version     string
	Description string
	Author      string
}

// Config holds common plugin configuration
type Config struct {
	LogLevel string
	Context  string
	APIUrl   string
	Format   string
}

// SDK provides common functionality for plugins
type SDK struct {
	Metadata Metadata
	Config   Config
	RootCmd  *cobra.Command
}

// NewSDK creates a new plugin SDK instance
func NewSDK(metadata Metadata) *SDK {
	sdk := &SDK{
		Metadata: metadata,
		Config:   Config{},
	}

	sdk.RootCmd = &cobra.Command{
		Use:     fmt.Sprintf("sitectl-plugin-%s", metadata.Name),
		Short:   metadata.Description,
		Version: metadata.Version,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			return sdk.setupLogging(cmd)
		},
	}

	sdk.addCommonFlags()
	return sdk
}

// setupLogging configures the logger based on flags
func (s *SDK) setupLogging(cmd *cobra.Command) error {
	level := slog.LevelInfo
	ll, err := cmd.Flags().GetString("log-level")
	if err != nil {
		return err
	}

	switch strings.ToUpper(ll) {
	case "DEBUG":
		level = slog.LevelDebug
	case "WARN":
		level = slog.LevelWarn
	case "ERROR":
		level = slog.LevelError
	}

	opts := &slog.HandlerOptions{
		Level: level,
	}
	handler := slog.New(slog.NewTextHandler(os.Stdout, opts))
	slog.SetDefault(handler)

	// Store config for plugin use
	s.Config.LogLevel = ll
	if s.RootCmd.PersistentFlags().Lookup("context") != nil {
		s.Config.Context, _ = cmd.Flags().GetString("context")
	}
	if s.RootCmd.PersistentFlags().Lookup("api-url") != nil {
		s.Config.APIUrl, _ = cmd.Flags().GetString("api-url")
	}
	if s.RootCmd.PersistentFlags().Lookup("format") != nil {
		s.Config.Format, _ = cmd.Flags().GetString("format")
	}

	return nil
}

// addCommonFlags adds standard flags to the plugin
func (s *SDK) addCommonFlags() {
	ll := os.Getenv("LOG_LEVEL")
	if ll == "" {
		ll = "INFO"
	}

	s.RootCmd.PersistentFlags().String("log-level", ll, "The logging level for the command")
	s.RootCmd.PersistentFlags().String("format", "table", `Format output using a custom template:
'table':            Print output in table format with column headers (default)
'table TEMPLATE':   Print output in table format using the given Go template
'json':             Print in JSON format
'TEMPLATE':         Print output using the given Go template`)
}

// AddCommand adds a subcommand to the plugin
func (s *SDK) AddCommand(cmd *cobra.Command) {
	s.RootCmd.AddCommand(cmd)
}

// Execute runs the plugin
func (s *SDK) Execute() {
	if err := s.RootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

// AddLibopsFlags adds common libops-specific flags
func (s *SDK) AddLibopsFlags(currentContext string) {
	apiURL := os.Getenv("LIBOPS_API_URL")
	if apiURL == "" {
		apiURL = "https://api.libops.io"
	}

	s.RootCmd.PersistentFlags().String("context", currentContext, "The sitectl context to use. See sitectl config --help for more info")
	s.RootCmd.PersistentFlags().String("api-url", apiURL, "Base URL of the libops API")
}

// GetMetadataCommand returns a command that displays plugin metadata
func (s *SDK) GetMetadataCommand() *cobra.Command {
	return &cobra.Command{
		Use:    "plugin-info",
		Short:  "Display plugin metadata",
		Hidden: true, // Hidden from normal help, used for plugin discovery
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("Name: %s\n", s.Metadata.Name)
			fmt.Printf("Version: %s\n", s.Metadata.Version)
			fmt.Printf("Description: %s\n", s.Metadata.Description)
			if s.Metadata.Author != "" {
				fmt.Printf("Author: %s\n", s.Metadata.Author)
			}
		},
	}
}

// GetDockerClient creates a Docker client respecting the sitectl context
// This is a helper for plugins that need to interact with Docker
// Returns the existing DockerClient which handles both local and remote contexts
func (s *SDK) GetDockerClient() (*docker.DockerClient, error) {
	ctx, err := s.GetContext()
	if err != nil {
		return nil, fmt.Errorf("failed to get context: %w", err)
	}

	return docker.GetDockerCli(ctx)
}

// GetContext loads the sitectl context configuration
// This is useful for plugins that need to access context-specific settings
// If no context is specified, returns the current context from config
func (s *SDK) GetContext() (*config.Context, error) {
	// Load the config
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}

	// Use specified context or current context
	contextName := s.Config.Context
	if contextName == "" {
		contextName = cfg.CurrentContext
	}

	if contextName == "" {
		return nil, fmt.Errorf("no context specified and no current context set")
	}

	// Find the context
	for _, ctx := range cfg.Contexts {
		if ctx.Name == contextName {
			return &ctx, nil
		}
	}

	return nil, fmt.Errorf("context %q not found", contextName)
}

// ExecInContainer executes a command in a Docker container
// This is a convenience wrapper for plugins
func (s *SDK) ExecInContainer(ctx context.Context, containerID string, cmd []string) (int, error) {
	cli, err := s.GetDockerClient()
	if err != nil {
		return -1, fmt.Errorf("failed to create Docker client: %w", err)
	}
	defer cli.Close()

	return cli.ExecSimple(ctx, containerID, cmd)
}

// ExecInContainerInteractive executes an interactive command in a Docker container with TTY
// This is a convenience wrapper for plugins
func (s *SDK) ExecInContainerInteractive(ctx context.Context, containerID string, cmd []string) (int, error) {
	cli, err := s.GetDockerClient()
	if err != nil {
		return -1, fmt.Errorf("failed to create Docker client: %w", err)
	}
	defer cli.Close()

	return cli.ExecInteractive(ctx, containerID, cmd)
}

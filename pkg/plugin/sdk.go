package plugin

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"charm.land/fang/v2"
	"github.com/libops/sitectl/pkg/component"
	"github.com/libops/sitectl/pkg/config"
	"github.com/libops/sitectl/pkg/docker"
	"github.com/libops/sitectl/pkg/validate"
	"github.com/spf13/cobra"
	"golang.org/x/crypto/ssh"
	"golang.org/x/term"
	yaml "gopkg.in/yaml.v3"
)

// Metadata contains information about a plugin
type Metadata struct {
	Name         string
	Version      string
	Description  string
	Author       string
	TemplateRepo string
	Includes     []string
}

var builtinPluginIncludes = map[string][]string{
	"isle": {"drupal"},
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
	Metadata          Metadata
	Config            Config
	RootCmd           *cobra.Command
	contextValidators []validate.Validator
	contextCache      *config.Context
	sshClient         *ssh.Client
	jobs              []RegisteredJob
	jobRootCmd        *cobra.Command
	creates           []RegisteredCreate
	createRootCmd     *cobra.Command
	componentDefs     []component.Definition
}

// NewSDK creates a new plugin SDK instance
func NewSDK(metadata Metadata) *SDK {
	sdk := &SDK{
		Metadata: metadata,
		Config:   Config{},
	}

	sdk.RootCmd = &cobra.Command{
		Use:     metadata.Name,
		Short:   metadata.Description,
		Version: metadata.Version,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			return sdk.setupLogging(cmd)
		},
		PersistentPostRun: func(cmd *cobra.Command, args []string) {
			sdk.Close()
		},
		Annotations: map[string]string{
			cobra.CommandDisplayNameAnnotation: fmt.Sprintf("sitectl %s", metadata.Name),
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
	handler := slog.New(slog.NewTextHandler(os.Stderr, opts))
	slog.SetDefault(handler)

	// Store config for plugin use.
	s.Config.LogLevel = ll
	if s.RootCmd.PersistentFlags().Lookup("context") != nil {
		s.Config.Context, _ = cmd.Flags().GetString("context")
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
	c, err := config.Current()
	if err != nil {
		slog.Warn("unable to fetch current context, defaulting to empty", "err", err)
	}

	s.RootCmd.PersistentFlags().String("context", c, "The sitectl context to use. See sitectl config --help for more info")
}

// AddCommand adds a subcommand to the plugin
func (s *SDK) AddCommand(cmd *cobra.Command) {
	s.RootCmd.AddCommand(cmd)
}

// Execute runs the plugin
func (s *SDK) Execute() {
	runCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-runCtx.Done()
		_ = s.Close()
	}()
	if err := fang.Execute(
		runCtx,
		s.RootCmd,
		fang.WithVersion(s.RootCmd.Version),
	); err != nil {
		os.Exit(1)
	}
}

// SetVersionInfo formats plugin version metadata like the main sitectl binary.
func (s *SDK) SetVersionInfo(version, commit, date string) {
	formatted := fmt.Sprintf("%s (Built on %s from Git SHA %s)", version, date, commit)
	s.Metadata.Version = formatted
	s.RootCmd.Version = formatted
}

// GetDiscoveryMetadataCommand returns a single cheap metadata payload for plugin discovery.
func (s *SDK) GetDiscoveryMetadataCommand() *cobra.Command {
	return &cobra.Command{
		Use:    "__plugin-metadata",
		Short:  "Display plugin discovery metadata",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			info := InstalledPlugin{
				Name:              s.Metadata.Name,
				BinaryName:        cmd.Root().Name(),
				Description:       s.Metadata.Description,
				Author:            s.Metadata.Author,
				TemplateRepo:      strings.TrimSpace(s.Metadata.TemplateRepo),
				Includes:          append([]string{}, s.Metadata.Includes...),
				CreateDefinitions: s.CreateDefinitions(),
			}
			info.CanCreate = len(info.CreateDefinitions) > 0
			if info.TemplateRepo == "" {
				if spec, ok := defaultCreateDefinition(info.CreateDefinitions); ok {
					info.TemplateRepo = strings.TrimSpace(spec.DockerComposeRepo)
				}
			}
			data, err := yaml.Marshal(info)
			if err != nil {
				return fmt.Errorf("marshal plugin discovery metadata: %w", err)
			}
			_, err = cmd.OutOrStdout().Write(data)
			return err
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
	if ctx.DockerHostType == config.ContextLocal {
		return docker.GetDockerCli(ctx)
	}
	sshClient, err := s.getSSHClient()
	if err != nil {
		return nil, err
	}
	return docker.GetDockerCliWithSSH(ctx, sshClient, false)
}

func (s *SDK) GetSSHClient() (*ssh.Client, error) {
	return s.getSSHClient()
}

// GetContext loads the sitectl context configuration
// This is useful for plugins that need to access context-specific settings
// If no context is specified, returns the current context from config
func (s *SDK) GetContext() (*config.Context, error) {
	if s.contextCache != nil {
		return s.contextCache, nil
	}
	// Load the config
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}

	// Use explicit --context if provided, otherwise resolve current context
	contextName := s.Config.Context
	if contextName == "" {
		contextName, err = config.Current()
		if err != nil {
			return nil, fmt.Errorf("failed to resolve current context: %w", err)
		}
	}

	if contextName == "" {
		return nil, fmt.Errorf("no context specified and no current context set")
	}

	// Find the context
	for _, ctx := range cfg.Contexts {
		if ctx.Name == contextName {
			if err := validateContextPlugin(ctx.Plugin, s.Metadata.Name); err != nil {
				return nil, fmt.Errorf("context %q is not supported by plugin %q: %w", ctx.Name, s.Metadata.Name, err)
			}
			s.contextCache = &ctx
			return s.contextCache, nil
		}
	}

	return nil, fmt.Errorf("context %q not found", contextName)
}

func (s *SDK) getSSHClient() (*ssh.Client, error) {
	if s.sshClient != nil {
		return s.sshClient, nil
	}
	ctx, err := s.GetContext()
	if err != nil {
		return nil, err
	}
	if ctx == nil || ctx.DockerHostType == config.ContextLocal {
		return nil, nil
	}
	s.sshClient, err = ctx.DialSSH()
	if err != nil {
		return nil, err
	}
	return s.sshClient, nil
}

func (s *SDK) Close() error {
	if s == nil {
		return nil
	}
	if s.sshClient != nil {
		err := s.sshClient.Close()
		s.sshClient = nil
		return err
	}
	return nil
}

func validateContextPlugin(contextPlugin, requestedPlugin string) error {
	contextPlugin = strings.TrimSpace(contextPlugin)
	requestedPlugin = strings.TrimSpace(requestedPlugin)

	if contextPlugin == "" {
		return fmt.Errorf("context plugin is empty")
	}
	if requestedPlugin == "" {
		return fmt.Errorf("requested plugin is empty")
	}
	if ContextPluginSupports(contextPlugin, requestedPlugin) {
		return nil
	}
	return fmt.Errorf("context plugin %q does not include %q", contextPlugin, requestedPlugin)
}

func ContextPluginSupports(contextPlugin, requestedPlugin string) bool {
	if contextPlugin == requestedPlugin {
		return true
	}

	visited := map[string]bool{}
	var walk func(string) bool
	walk = func(plugin string) bool {
		if visited[plugin] {
			return false
		}
		visited[plugin] = true
		for _, included := range pluginIncludes(plugin) {
			if included == requestedPlugin || walk(included) {
				return true
			}
		}
		return false
	}

	return walk(contextPlugin)
}

func pluginIncludes(plugin string) []string {
	seen := map[string]bool{}
	includes := make([]string, 0, len(builtinPluginIncludes[plugin]))

	for _, include := range builtinPluginIncludes[plugin] {
		if include == "" || seen[include] {
			continue
		}
		seen[include] = true
		includes = append(includes, include)
	}

	if installed, ok := FindInstalled(plugin); ok {
		for _, include := range installed.Includes {
			if include == "" || seen[include] {
				continue
			}
			seen[include] = true
			includes = append(includes, include)
		}
	}

	return includes
}

// GetComponentManager creates a component manager bound to the active sitectl context.
func (s *SDK) GetComponentManager() (*component.Manager, error) {
	ctx, err := s.GetContext()
	if err != nil {
		return nil, fmt.Errorf("failed to get context: %w", err)
	}

	return component.NewManager(ctx), nil
}

func (s *SDK) RegisterContextValidator(validator validate.Validator) {
	if validator == nil {
		return
	}
	s.contextValidators = append(s.contextValidators, validator)
}

func (s *SDK) ContextValidators() []validate.Validator {
	out := make([]validate.Validator, len(s.contextValidators))
	copy(out, s.contextValidators)
	return out
}

// PromptAndSaveLocalContext creates or updates a local sitectl context using
// the shared config prompts and save behavior.
func (s *SDK) PromptAndSaveLocalContext(opts config.LocalContextCreateOptions) (*config.Context, error) {
	return config.PromptAndSaveLocalContext(opts)
}

// ExecContainer executes a command in a Docker container using the shared SDK Docker path.
func (s *SDK) ExecContainer(ctx context.Context, opts docker.ExecOptions) (int, error) {
	cli, err := s.GetDockerClient()
	if err != nil {
		return -1, fmt.Errorf("failed to create Docker client: %w", err)
	}
	defer cli.Close()

	return cli.Exec(ctx, opts)
}

// ExecInContainer executes a command in a Docker container.
// This is a convenience wrapper for plugins.
func (s *SDK) ExecInContainer(ctx context.Context, containerID string, cmd []string) (int, error) {
	return s.ExecContainer(ctx, docker.ExecOptions{
		Container:    containerID,
		Cmd:          cmd,
		AttachStdout: true,
		AttachStderr: true,
	})
}

// ExecInContainerInteractive executes an interactive command in a Docker container with TTY.
// This is a convenience wrapper for plugins.
func (s *SDK) ExecInContainerInteractive(ctx context.Context, containerID string, cmd []string) (int, error) {
	return s.ExecContainer(ctx, docker.ExecOptions{
		Container:    containerID,
		Cmd:          cmd,
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
		Tty:          true,
	})
}

type CommandExecOptions struct {
	Context    context.Context
	Stdin      io.Reader
	Stdout     io.Writer
	Stderr     io.Writer
	Capture    bool
	LiveStderr bool
}

func (s *SDK) InvokePluginCommand(pluginName string, args []string, opts CommandExecOptions) (string, error) {
	installed, ok := FindInstalled(pluginName)
	if !ok {
		return "", fmt.Errorf("plugin %q is not installed", pluginName)
	}

	invocation := make([]string, 0, len(args)+4)
	if strings.TrimSpace(s.Config.Context) != "" {
		invocation = append(invocation, "--context", s.Config.Context)
	}
	if strings.TrimSpace(s.Config.LogLevel) != "" {
		invocation = append(invocation, "--log-level", s.Config.LogLevel)
	}
	invocation = append(invocation, args...)
	slog.Debug("invoking plugin command", "plugin", pluginName, "path", installed.Path, "args", invocation, "capture", opts.Capture)

	execCtx := opts.Context
	if execCtx == nil {
		execCtx = context.Background()
	}
	cmd := exec.CommandContext(execCtx, installed.Path, invocation...)
	cmd.Env = os.Environ()
	if width, ok := terminalColumns(); ok {
		cmd.Env = append(cmd.Env, fmt.Sprintf("COLUMNS=%d", width))
	}
	cmd.Stdin = opts.Stdin
	cmd.Stderr = opts.Stderr

	if opts.Capture {
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		cmd.Stdout = &stdout
		var stderrSink io.Writer
		if opts.Stderr != nil && opts.LiveStderr {
			stderrSink = io.MultiWriter(opts.Stderr, &stderr)
		} else if opts.LiveStderr {
			stderrSink = io.MultiWriter(os.Stderr, &stderr)
		} else {
			stderrSink = &stderr
		}
		cmd.Stderr = stderrSink
		if err := cmd.Run(); err != nil {
			detail := strings.TrimSpace(stderr.String())
			if detail == "" {
				detail = strings.TrimSpace(stdout.String())
			}
			if detail != "" {
				return "", fmt.Errorf("run plugin %q: %w: %s", pluginName, err, detail)
			}
			return "", fmt.Errorf("run plugin %q: %w", pluginName, err)
		}
		slog.Debug("plugin command completed", "plugin", pluginName, "path", installed.Path)
		return stdout.String(), nil
	}

	if opts.Stdout != nil {
		cmd.Stdout = opts.Stdout
	}
	if cmd.Stdout == nil {
		cmd.Stdout = os.Stdout
	}
	if cmd.Stderr == nil {
		cmd.Stderr = os.Stderr
	}
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("run plugin %q: %w", pluginName, err)
	}
	slog.Debug("plugin command completed", "plugin", pluginName, "path", installed.Path)
	return "", nil
}

func (s *SDK) InvokeIncludedPluginCommand(pluginName string, args []string, opts CommandExecOptions) (string, error) {
	allowed := false
	for _, include := range s.Metadata.Includes {
		if include == pluginName {
			allowed = true
			break
		}
	}
	if !allowed {
		return "", fmt.Errorf("plugin %q is not included by %q", pluginName, s.Metadata.Name)
	}

	return s.InvokePluginCommand(pluginName, args, opts)
}

func (s *SDK) InvokeIncludedPlugins(args []string) ([]string, error) {
	outputs := make([]string, 0, len(s.Metadata.Includes))
	for _, include := range s.Metadata.Includes {
		output, err := s.InvokeIncludedPluginCommand(include, args, CommandExecOptions{Capture: true})
		if err != nil {
			return nil, err
		}
		if trimmed := strings.TrimSpace(output); trimmed != "" {
			outputs = append(outputs, trimmed)
		}
	}
	return outputs, nil
}

func terminalColumns() (int, bool) {
	if columns, err := strconv.Atoi(strings.TrimSpace(os.Getenv("COLUMNS"))); err == nil && columns > 0 {
		return columns, true
	}
	if width, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && width > 0 {
		return width, true
	}
	return 0, false
}

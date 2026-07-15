package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"charm.land/fang/v2"
	"github.com/libops/sitectl/pkg/component"
	"github.com/libops/sitectl/pkg/config"
	"github.com/libops/sitectl/pkg/docker"
	"github.com/libops/sitectl/pkg/validate"
	"github.com/spf13/cobra"
	"golang.org/x/crypto/ssh"
	"golang.org/x/term"
)

const pluginRPCProcessWaitDelay = 500 * time.Millisecond

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

// Config holds common plugin configuration.
type Config struct {
	LogLevel string
	Context  string
	APIUrl   string
	Format   string
}

// SDK provides common functionality for plugins.
//
// Command registration and RPC dispatch mutate cobra command state and are not
// safe to fan out through one SDK concurrently. The SDK guards its context and
// SSH connection caches so host-side discovery helpers can reuse them safely.
type SDK struct {
	Metadata Metadata
	// Config is updated during normal command setup and single-shot RPC
	// dispatch. Do not treat one SDK value as immutable across RPC requests.
	Config                      Config
	RootCmd                     *cobra.Command
	contextValidators           []validate.Validator
	mu                          sync.Mutex
	contextCache                *config.Context
	sshClient                   *ssh.Client
	jobs                        []RegisteredJob
	jobRootCmd                  *cobra.Command
	creates                     []RegisteredCreate
	createRootCmd               *cobra.Command
	componentRootCmd            *cobra.Command
	debugCmd                    *cobra.Command
	convergeCmd                 *cobra.Command
	setCmd                      *cobra.Command
	validateCmd                 *cobra.Command
	validateRunner              ValidateRunner
	healthcheckCmd              *cobra.Command
	healthcheckRunner           HealthcheckRunner
	ingressRoutesCmd            *cobra.Command
	ingressRouteProvider        IngressRouteProvider
	verifyCmd                   *cobra.Command
	verifyRunner                VerifyRunner
	componentDefs               []component.Definition
	serviceComponents           []component.ComposeServiceComponent
	serviceComponentDisplayName string
	deploys                     []RegisteredDeploy
	deployRootCmd               *cobra.Command
	projectDiscovery            ProjectDiscoveryFunc
	hasDebug                    bool
	hasConverge                 bool
	hasSet                      bool
	hasValidate                 bool
	hasHealthcheck              bool
	hasIngressRoutes            bool
	hasVerify                   bool
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
			if err := sdk.Close(); err != nil {
				slog.Debug("close sdk", "error", err)
			}
		},
		Annotations: map[string]string{
			cobra.CommandDisplayNameAnnotation: fmt.Sprintf("sitectl %s", metadata.Name),
		},
	}

	sdk.addCommonFlags()
	sdk.RootCmd.AddCommand(sdk.GetRPCCommand())
	config.SetProjectClaimDetector(sdk.detectProjectOwner)
	return sdk
}

// setupLogging configures the logger based on flags
func (s *SDK) setupLogging(cmd *cobra.Command) error {
	ll, err := cmd.Flags().GetString("log-level")
	if err != nil {
		return err
	}
	setupPluginLogger(ll)

	contextName := ""
	if s.RootCmd.PersistentFlags().Lookup("context") != nil && cmd.Flags().Changed("context") {
		contextName, _ = cmd.Flags().GetString("context")
	}

	// Store config for plugin use.
	s.mu.Lock()
	s.Config.LogLevel = ll
	s.Config.Context = contextName
	s.mu.Unlock()

	return nil
}

func setupPluginLogger(ll string) {
	level := slog.LevelInfo
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
}

// addCommonFlags adds standard flags to the plugin
func (s *SDK) addCommonFlags() {
	ll := os.Getenv("LOG_LEVEL")
	if ll == "" {
		ll = "INFO"
	}
	s.RootCmd.PersistentFlags().String("log-level", ll, "The logging level for the command")
	s.RootCmd.PersistentFlags().String("context", "", "The sitectl context to use. See sitectl config --help for more info")
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

// GetSSHClient returns an SSH client for the resolved sitectl context.
func (s *SDK) GetSSHClient() (*ssh.Client, error) {
	return s.getSSHClient()
}

// GetContext loads the sitectl context configuration
// This is useful for plugins that need to access context-specific settings
// If no context is specified, returns the current context from config
func (s *SDK) GetContext() (*config.Context, error) {
	s.mu.Lock()
	if s.contextCache != nil {
		ctx := s.contextCache
		s.mu.Unlock()
		return ctx, nil
	}
	contextName := s.Config.Context
	s.mu.Unlock()

	// Load the config
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}

	// Use explicit --context if provided, otherwise resolve current context
	if contextName == "default" {
		contextName = cfg.CurrentContext
	}
	if contextName == "" {
		contextName, err = config.CurrentForPluginWithDiagnostics(s.Metadata.Name, os.Stderr)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve current context: %w", err)
		}
	}

	if contextName == "" {
		return nil, fmt.Errorf("no context specified and no current context set")
	}

	ctx, err := config.GetContextForPlugin(contextName, s.Metadata.Name)
	if err != nil {
		return nil, err
	}
	if err := validateContextPlugin(ctx.Plugin, s.Metadata.Name); err != nil {
		return nil, fmt.Errorf("context %q is not supported by plugin %q: %w", ctx.Name, s.Metadata.Name, err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.contextCache != nil {
		return s.contextCache, nil
	}
	s.contextCache = &ctx
	return s.contextCache, nil
}

func (s *SDK) getSSHClient() (*ssh.Client, error) {
	s.mu.Lock()
	if s.sshClient != nil {
		client := s.sshClient
		s.mu.Unlock()
		return client, nil
	}
	s.mu.Unlock()

	ctx, err := s.GetContext()
	if err != nil {
		return nil, err
	}
	if ctx == nil || ctx.DockerHostType == config.ContextLocal {
		return nil, nil
	}
	client, err := ctx.DialSSH()
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sshClient != nil {
		_ = client.Close()
		return s.sshClient, nil
	}
	s.sshClient = client
	return s.sshClient, nil
}

func (s *SDK) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	client := s.sshClient
	s.sshClient = nil
	s.mu.Unlock()
	if client != nil {
		return client.Close()
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

// CommandExecOptions controls subprocess execution for plugin RPC calls.
// Stdout is always captured into RPCResponse.Output. LiveStdout mirrors command
// stdout to the plugin process stderr while still capturing it, because process
// stdout is reserved for the final RPC response envelope. Use Stdin only for
// interactive RPC methods; when Stdin is nil the host sends the RPC envelope
// over stdin instead of argv. When Stdin is set, the request is encoded into
// argv so request args and params may be visible in process listings; never
// put secrets in any RPCRequest field. The argv safety gate validates typed
// Params only; Args, Context, and LogLevel are copied as caller-provided values.
type CommandExecOptions struct {
	Context context.Context
	// Stdin preserves interactive stdin for the plugin. Setting it moves the
	// RPC request envelope to argv. Params marked sensitive are rejected, but
	// request args are not machine-checked and must not contain secrets.
	Stdin      io.Reader
	Stderr     io.Writer
	LiveStderr bool
	LiveStdout bool
}

type pluginRPCPathOptions struct {
	CommandExecOptions
	ExtraEnv []string
}

// RPCProcessError reports a plugin subprocess failure before a valid RPC
// response envelope was returned.
type RPCProcessError struct {
	Plugin   string
	Method   string
	ExitCode int
	Detail   string
	Err      error
}

// Error formats the process failure with any captured stderr/stdout detail.
func (e *RPCProcessError) Error() string {
	if e == nil {
		return "unknown plugin rpc process failure"
	}
	message := fmt.Sprintf("run plugin rpc %q %s", e.Plugin, e.Method)
	if e.Err != nil {
		message = fmt.Sprintf("%s: %v", message, e.Err)
	}
	if detail := strings.TrimSpace(e.Detail); detail != "" {
		message = fmt.Sprintf("%s: %s", message, detail)
	}
	return message
}

// Unwrap returns the underlying exec error.
func (e *RPCProcessError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// InvokePluginRPC invokes an installed sitectl plugin through the private RPC
// entrypoint and returns the decoded response envelope.
//
// When opts.Stdin is set, the request envelope is encoded into argv so the
// plugin can keep stdin interactive. Args and params in that mode may be
// visible in process listings; never put secrets in any RPC field. Requests
// built from params marked with RPCSensitiveParams or `rpc_sensitive:"true"`
// are rejected before the subprocess starts; Args, Context, and LogLevel remain
// caller-trusted and are not inspected by that sensitivity check.
func (s *SDK) InvokePluginRPC(pluginName string, req RPCRequest, opts CommandExecOptions) (RPCResponse, error) {
	installed, ok := FindInstalled(pluginName)
	if !ok {
		return RPCResponse{}, fmt.Errorf("plugin %q is not installed", pluginName)
	}
	return s.invokePluginRPCPath(pluginName, installed.Path, req, opts)
}

func (s *SDK) invokePluginRPCPath(pluginName, pluginPath string, req RPCRequest, opts CommandExecOptions) (RPCResponse, error) {
	req = s.defaultRPCRequest(req)
	slog.Debug("invoking plugin rpc", "plugin", pluginName, "path", pluginPath, "method", req.Method)
	return runPluginRPCPath(pluginName, pluginPath, req, pluginRPCPathOptions{CommandExecOptions: opts})
}

func (s *SDK) defaultRPCRequest(req RPCRequest) RPCRequest {
	req.ProtocolVersion = normalizeRPCProtocolVersion(req.ProtocolVersion)
	s.mu.Lock()
	contextName := s.Config.Context
	logLevel := s.Config.LogLevel
	s.mu.Unlock()
	if strings.TrimSpace(req.Context) == "" {
		req.Context = strings.TrimSpace(contextName)
	}
	if strings.TrimSpace(req.LogLevel) == "" {
		req.LogLevel = strings.TrimSpace(logLevel)
	}
	return req
}

func runPluginRPCPath(pluginName, pluginPath string, req RPCRequest, opts pluginRPCPathOptions) (RPCResponse, error) {
	req.ProtocolVersion = normalizeRPCProtocolVersion(req.ProtocolVersion)
	execCtx := opts.Context
	if execCtx == nil {
		execCtx = context.Background()
	}
	var cmd *exec.Cmd
	if opts.Stdin != nil {
		if err := ensureRPCRequestArgvSafe(req); err != nil {
			return RPCResponse{}, err
		}
		encoded, err := encodeRPCRequestFlag(req)
		if err != nil {
			return RPCResponse{}, err
		}
		// Preserve stdin for interactive plugin handlers. The request is visible
		// in process listings in this mode, so callers must not put secrets in
		// RPC args or params.
		cmd = exec.CommandContext(execCtx, pluginPath, "__sitectl-rpc", "--request", encoded) // #nosec G204,G702 -- plugin executable comes from trusted PATH discovery and args are a JSON envelope.
		cmd.Stdin = opts.Stdin
	} else {
		requestData, err := json.Marshal(req)
		if err != nil {
			return RPCResponse{}, fmt.Errorf("marshal rpc request: %w", err)
		}
		cmd = exec.CommandContext(execCtx, pluginPath, "__sitectl-rpc") // #nosec G204,G702 -- plugin executable comes from trusted PATH discovery and receives a JSON envelope on stdin.
		cmd.Stdin = bytes.NewReader(requestData)
	}
	cmd.Env = append(os.Environ(), "SITECTL_RPC=1")
	// Metadata discovery sends the request over stdin, so argv-only detection
	// cannot see the method. This env marker keeps plugin startup on the cheap
	// metadata path without consuming stdin.
	if req.Method == MethodPluginMetadata {
		cmd.Env = append(cmd.Env, "SITECTL_RPC_METADATA=1")
	}
	if opts.LiveStdout {
		cmd.Env = append(cmd.Env, "SITECTL_RPC_LIVE_STDOUT=1")
	}
	cmd.Env = append(cmd.Env, opts.ExtraEnv...)
	if width, ok := terminalColumns(); ok {
		cmd.Env = append(cmd.Env, fmt.Sprintf("COLUMNS=%d", width))
	}
	// Reserved build identity values always come from the running sitectl host,
	// never from inherited environment or internal RPC options.
	cmd.Env = filterHostBuildEnvironment(cmd.Env)
	cmd.WaitDelay = pluginRPCProcessWaitDelay

	stdout := newLimitedRPCBuffer(maxRPCResponseBytes)
	var stderr bytes.Buffer
	cmd.Stdout = stdout
	var stderrSink io.Writer
	if opts.Stderr != nil && (opts.LiveStderr || opts.LiveStdout) {
		stderrSink = io.MultiWriter(opts.Stderr, &stderr)
	} else if opts.LiveStderr || opts.LiveStdout {
		stderrSink = io.MultiWriter(os.Stderr, &stderr)
	} else {
		stderrSink = &stderr
	}
	cmd.Stderr = stderrSink

	if err := cmd.Run(); err != nil {
		if ctxErr := execCtx.Err(); ctxErr != nil {
			err = fmt.Errorf("%w: %w", ctxErr, err)
		}
		return RPCResponse{}, newRPCProcessError(pluginName, req.Method, err, stdout.String(), stderr.String())
	}
	if stdout.Exceeded() {
		return RPCResponse{}, fmt.Errorf("plugin %q rpc response for method %s exceeds %d bytes; reduce output, use --format json when available, or narrow the request", pluginName, req.Method, maxRPCResponseBytes)
	}

	resp, err := parsePluginRPCResponse(pluginName, req.Method, stdout.Bytes())
	if err != nil {
		return RPCResponse{}, err
	}
	resp.ProtocolVersion = normalizeRPCProtocolVersion(resp.ProtocolVersion)
	if resp.ProtocolVersion != RPCProtocolVersion {
		return RPCResponse{}, fmt.Errorf("plugin %q returned %s", pluginName, rpcProtocolVersionMismatchMessage(resp.ProtocolVersion))
	}
	if !resp.OK {
		return resp, newRPCFailure(pluginName, req.Method, resp, nil)
	}
	return resp, nil
}

func parsePluginRPCResponse(pluginName, method string, data []byte) (RPCResponse, error) {
	trimmed := bytes.TrimSpace(data)
	if resp, err := decodePluginRPCEnvelope(trimmed); err == nil {
		return resp, nil
	} else {
		lastLine, fallbackOffset := lastNonEmptyRPCLine(data)
		if len(lastLine) > 0 && !bytes.Equal(lastLine, trimmed) {
			if resp, lineErr := decodePluginRPCEnvelope(lastLine); lineErr == nil {
				slog.Warn("plugin wrote stdout outside rpc envelope; fallback parsing is best-effort and handlers must use cmd.OutOrStdout()", "plugin", pluginName, "method", method, "fallback_offset", fallbackOffset, "stdout_prefix_bytes", fallbackOffset)
				return resp, nil
			}
		}
		return RPCResponse{}, fmt.Errorf("parse plugin rpc response from %q %s: %w; plugin wrote non-JSON to stdout or a non-envelope JSON line; stdout fallback parsing is best-effort and handlers must use cmd.OutOrStdout(): %s", pluginName, method, err, rpcStdoutSnippet(data))
	}
}

func decodePluginRPCEnvelope(data []byte) (RPCResponse, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return RPCResponse{}, err
	}
	if _, ok := fields["ok"]; !ok {
		return RPCResponse{}, fmt.Errorf("rpc response envelope missing ok field")
	}
	var resp RPCResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return RPCResponse{}, err
	}
	return resp, nil
}

func lastNonEmptyRPCLine(data []byte) ([]byte, int) {
	lineEnd := len(data)
	for lineEnd > 0 {
		if data[lineEnd-1] == '\n' {
			lineEnd--
		}
		lineStart := bytes.LastIndexByte(data[:lineEnd], '\n') + 1
		rawLine := data[lineStart:lineEnd]
		line := bytes.TrimSpace(rawLine)
		if len(line) != 0 {
			return line, lineStart + bytes.Index(rawLine, line)
		}
		if lineStart == 0 {
			break
		}
		lineEnd = lineStart - 1
	}
	return nil, -1
}

func rpcStdoutSnippet(data []byte) string {
	const maxSnippet = 2048
	trimmed := strings.TrimSpace(string(data))
	if len(trimmed) <= maxSnippet {
		return trimmed
	}
	return trimmed[:maxSnippet] + "...<truncated>"
}

func newRPCProcessError(pluginName, method string, err error, stdout, stderr string) *RPCProcessError {
	detail := strings.TrimSpace(stderr)
	if detail == "" {
		detail = strings.TrimSpace(stdout)
	}
	processErr := &RPCProcessError{
		Plugin:   pluginName,
		Method:   method,
		ExitCode: -1,
		Detail:   detail,
		Err:      err,
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		processErr.ExitCode = exitErr.ExitCode()
	}
	return processErr
}

// InvokeIncludedPluginRPC invokes a plugin declared in this SDK's Includes
// metadata through the private RPC entrypoint.
func (s *SDK) InvokeIncludedPluginRPC(pluginName string, req RPCRequest, opts CommandExecOptions) (RPCResponse, error) {
	allowed := false
	for _, include := range s.Metadata.Includes {
		if include == pluginName {
			allowed = true
			break
		}
	}
	if !allowed {
		return RPCResponse{}, fmt.Errorf("plugin %q is not included by %q", pluginName, s.Metadata.Name)
	}
	return s.InvokePluginRPC(pluginName, req, opts)
}

// InvokeIncludedPluginJob invokes a registered job on an included plugin.
func (s *SDK) InvokeIncludedPluginJob(pluginName, jobName string, args []string, opts CommandExecOptions) (RPCResponse, error) {
	return s.InvokeIncludedPluginJobContext(pluginName, "", jobName, args, opts)
}

// InvokeIncludedPluginJobContext invokes a registered job on an included plugin
// against a specific sitectl context.
func (s *SDK) InvokeIncludedPluginJobContext(pluginName, contextName, jobName string, args []string, opts CommandExecOptions) (RPCResponse, error) {
	req, err := NewJobRunRequest(jobName, args...)
	if err != nil {
		return RPCResponse{}, err
	}
	req.Context = strings.TrimSpace(contextName)
	return s.InvokeIncludedPluginRPC(pluginName, req, opts)
}

// DecodeRPCResult unmarshals the structured result payload from an RPC
// response into the requested Go type.
func DecodeRPCResult[T any](resp RPCResponse) (T, error) {
	var out T
	if len(resp.Result) == 0 {
		return out, nil
	}
	if err := json.Unmarshal(resp.Result, &out); err != nil {
		return out, fmt.Errorf("parse rpc result: %w", err)
	}
	return out, nil
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

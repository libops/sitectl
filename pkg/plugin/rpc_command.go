package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"

	corejob "github.com/libops/sitectl/pkg/job"
	sitevalidate "github.com/libops/sitectl/pkg/validate"
	"github.com/spf13/cobra"
)

// GetRPCCommand returns the single private host/plugin entrypoint.
//
// The returned command is a single-shot subprocess entrypoint. RPC dispatch
// mutates SDK config and cobra command state while executing one request; do
// not reuse it for multiple requests in the same process. Plugin handlers must
// write user-visible output through cmd.OutOrStdout(); direct os.Stdout writes
// corrupt the JSON response envelope reserved for host/plugin framing.
//
// Correct handlers use cobra's output stream, for example:
//
//	_, err := fmt.Fprintln(cmd.OutOrStdout(), output)
//	return err
func (s *SDK) GetRPCCommand() *cobra.Command {
	var encoded string
	handled := false
	cmd := &cobra.Command{
		Use:          "__sitectl-rpc",
		Short:        "Run a sitectl plugin RPC request",
		Hidden:       true,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if handled {
				return writeRPCResponse(cmd, rpcErrorResponse(fmt.Errorf("__sitectl-rpc command already handled a request")))
			}
			handled = true
			req, err := rpcRequestFromCommand(cmd, encoded)
			if err != nil {
				return writeRPCResponse(cmd, rpcErrorResponse(newRPCError(RPCErrorCodeDecodeRequest, err)))
			}
			defer func() { _ = s.Close() }()
			resp, err := s.handleRPC(cmd, req)
			if err != nil {
				resp = rpcErrorResponseWithOutput(err, resp.Output)
			}
			return writeRPCResponse(cmd, resp)
		},
	}
	cmd.Flags().StringVar(&encoded, "request", "", "Base64-encoded JSON RPC request. Reads JSON from stdin when empty.")
	return cmd
}

func rpcRequestFromCommand(cmd *cobra.Command, encoded string) (RPCRequest, error) {
	if strings.TrimSpace(encoded) != "" {
		return decodeRPCRequestFlag(encoded)
	}
	return readRPCRequest(cmd.InOrStdin())
}

func writeRPCResponse(cmd *cobra.Command, resp RPCResponse) error {
	resp.ProtocolVersion = normalizeRPCProtocolVersion(resp.ProtocolVersion)
	data, err := json.Marshal(resp)
	if err != nil {
		return err
	}
	_, err = cmd.OutOrStdout().Write(append(data, '\n'))
	return err
}

func (s *SDK) handleRPC(cmd *cobra.Command, req RPCRequest) (RPCResponse, error) {
	// RPC dispatch is single-shot per plugin process. It mutates SDK config and
	// may execute cobra command singletons whose Changed flag state is not reset.
	// Returned errors are envelope, dispatch, params, or typed-handler failures.
	// Cobra command execution failures are owned by rpcCommand and are returned
	// as non-OK RPCResponse values with plugin_error details and captured output.
	//
	// Plugin-owned commands intentionally bridge typed params back into cobra
	// argv before running the registered command. That preserves existing runner
	// BindFlags/Run behavior, hooks, help semantics, and plugin passthrough flags
	// while keeping the host/plugin wire format typed and JSON-based.
	// Handler output must go through cobra's output streams so executeRPCCommand
	// can capture it inside the response envelope; os.Stdout is reserved for the
	// final JSON response written by __sitectl-rpc.
	contextName := strings.TrimSpace(req.Context)
	logLevel := strings.TrimSpace(req.LogLevel)
	s.mu.Lock()
	// Non-empty request values are authoritative for SDK-visible config.
	// Logger setup still only runs when the request explicitly carries a level.
	if contextName != "" {
		s.Config.Context = contextName
	}
	if logLevel != "" {
		s.Config.LogLevel = logLevel
	}
	s.mu.Unlock()
	if logLevel != "" {
		setupPluginLogger(logLevel)
	}

	spec, ok := rpcMethodFor(req.Method)
	if !ok {
		return RPCResponse{}, newRPCError(RPCErrorCodeUnsupportedMethod, fmt.Errorf("unsupported rpc method %q", req.Method))
	}
	return spec.handle(s, cmd, req)
}

type rpcMethodSpec struct {
	// paramsType is used by contract tests to assert each method decodes its declared params type.
	paramsType      reflect.Type
	paramsSensitive func(json.RawMessage) (bool, error)
	handle          func(*SDK, *cobra.Command, RPCRequest) (RPCResponse, error)
}

var (
	rpcMethodRegistryOnce sync.Once
	rpcMethodSpecs        map[string]rpcMethodSpec
)

func rpcMethodRegistry() map[string]rpcMethodSpec {
	rpcMethodRegistryOnce.Do(func() {
		rpcMethodSpecs = buildRPCMethodRegistry()
	})
	return rpcMethodSpecs
}

func buildRPCMethodRegistry() map[string]rpcMethodSpec {
	return map[string]rpcMethodSpec{
		MethodPluginMetadata: rpcMethodWithoutParams(func(s *SDK, cmd *cobra.Command, req RPCRequest) (RPCResponse, error) {
			return rpcResponse(s.discoveryMetadata(), "")
		}),
		MethodProjectDetect: rpcMethodWithParams[ProjectDetectParams](func(s *SDK, cmd *cobra.Command, req RPCRequest, params ProjectDetectParams) (RPCResponse, error) {
			resp, err := s.rpcProjectDetect(params.Path)
			if err != nil {
				return RPCResponse{}, fmt.Errorf("detect project: %w", err)
			}
			return resp, nil
		}),
		MethodCreateComponentDefinitions: rpcMethodWithoutParams(func(s *SDK, cmd *cobra.Command, req RPCRequest) (RPCResponse, error) {
			defs, err := s.CreateComponentDefinitions()
			if err != nil {
				return RPCResponse{}, fmt.Errorf("list create component definitions: %w", err)
			}
			return rpcResponse(defs, "")
		}),
		MethodCreateRun: rpcMethodWithParams[CreateRunParams](func(s *SDK, cmd *cobra.Command, req RPCRequest, params CreateRunParams) (RPCResponse, error) {
			return s.rpcCommand(cmd, req.Method, s.createRootCmd, append([]string{params.Name}, req.Args...))
		}),
		MethodDeployRun: rpcMethodWithParams[DeployRunParams](func(s *SDK, cmd *cobra.Command, req RPCRequest, params DeployRunParams) (RPCResponse, error) {
			return s.rpcCommand(cmd, req.Method, s.deployRootCmd, append([]string{params.Hook}, req.Args...))
		}),
		MethodJobList: rpcMethodWithoutParams(func(s *SDK, cmd *cobra.Command, req RPCRequest) (RPCResponse, error) {
			return s.rpcJobList()
		}),
		MethodJobRun: rpcMethodWithParams[JobRunParams](func(s *SDK, cmd *cobra.Command, req RPCRequest, params JobRunParams) (RPCResponse, error) {
			return s.rpcCommand(cmd, req.Method, s.jobRootCmd, append([]string{params.Name}, req.Args...))
		}),
		MethodComponentList: rpcMethodWithParams[ComponentListParams](func(s *SDK, cmd *cobra.Command, req RPCRequest, params ComponentListParams) (RPCResponse, error) {
			args := []string{"list"}
			if strings.TrimSpace(params.Name) != "" {
				args = append(args, params.Name)
			}
			return s.rpcCommand(cmd, req.Method, s.componentRootCmd, args)
		}),
		MethodComponentDescribe: rpcMethodWithParams[ComponentTargetParams](func(s *SDK, cmd *cobra.Command, req RPCRequest, params ComponentTargetParams) (RPCResponse, error) {
			return s.rpcComponentCommand(cmd, "describe", req, params)
		}),
		MethodComponentReconcile: rpcMethodWithParams[ComponentTargetParams](func(s *SDK, cmd *cobra.Command, req RPCRequest, params ComponentTargetParams) (RPCResponse, error) {
			return s.rpcComponentCommand(cmd, "reconcile", req, params)
		}),
		MethodComponentSet: rpcMethodWithParams[ComponentSetParams](func(s *SDK, cmd *cobra.Command, req RPCRequest, params ComponentSetParams) (RPCResponse, error) {
			args, err := componentSetArgs(params, req.Args)
			if err != nil {
				return RPCResponse{}, fmt.Errorf("build %s argv: %w", req.Method, err)
			}
			return s.rpcCommand(cmd, req.Method, s.componentRootCmd, args)
		}),
		MethodValidateRun: rpcMethodWithParams[ValidateRunParams](func(s *SDK, cmd *cobra.Command, req RPCRequest, params ValidateRunParams) (RPCResponse, error) {
			return s.rpcValidate(cmd, req, params)
		}),
		MethodHealthcheckRun: rpcMethodWithParams[HealthcheckRunParams](func(s *SDK, cmd *cobra.Command, req RPCRequest, params HealthcheckRunParams) (RPCResponse, error) {
			return s.rpcHealthcheck(cmd, req, params)
		}),
		MethodDebugRun: rpcMethodWithParams[DebugRunParams](func(s *SDK, cmd *cobra.Command, req RPCRequest, params DebugRunParams) (RPCResponse, error) {
			args, err := flagOnlyRPCArgs(nil, params, req.Args)
			if err != nil {
				return RPCResponse{}, fmt.Errorf("build %s argv: %w", req.Method, err)
			}
			return s.rpcCommand(cmd, req.Method, s.debugCmd, args)
		}),
		MethodSetRun: rpcMethodWithParams[SetRunParams](func(s *SDK, cmd *cobra.Command, req RPCRequest, params SetRunParams) (RPCResponse, error) {
			args, err := flagOnlyRPCArgs(nil, params, req.Args)
			if err != nil {
				return RPCResponse{}, fmt.Errorf("build %s argv: %w", req.Method, err)
			}
			return s.rpcCommand(cmd, req.Method, s.setCmd, args)
		}),
		MethodConvergeRun: rpcMethodWithParams[ConvergeRunParams](func(s *SDK, cmd *cobra.Command, req RPCRequest, params ConvergeRunParams) (RPCResponse, error) {
			args, err := flagOnlyRPCArgs(s.convergeCmd, params, req.Args)
			if err != nil {
				return RPCResponse{}, fmt.Errorf("build %s argv: %w", req.Method, err)
			}
			return s.rpcCommand(cmd, req.Method, s.convergeCmd, args)
		}),
	}
}

func rpcMethodFor(method string) (rpcMethodSpec, bool) {
	spec, ok := rpcMethodRegistry()[method]
	return spec, ok
}

func rpcMethodWithoutParams(handler func(*SDK, *cobra.Command, RPCRequest) (RPCResponse, error)) rpcMethodSpec {
	return rpcMethodSpec{handle: handler}
}

func rpcMethodWithParams[T any](handler func(*SDK, *cobra.Command, RPCRequest, T) (RPCResponse, error)) rpcMethodSpec {
	return rpcMethodSpec{
		paramsType:      reflect.TypeOf((*T)(nil)).Elem(),
		paramsSensitive: rpcDecodedParamsAreSensitive[T],
		handle: func(s *SDK, cmd *cobra.Command, req RPCRequest) (RPCResponse, error) {
			params, err := DecodeRPCParams[T](req.Params)
			if err != nil {
				return RPCResponse{}, newRPCError(RPCErrorCodeDecodeParams, fmt.Errorf("decode %s params: %w", req.Method, err))
			}
			return handler(s, cmd, req, params)
		},
	}
}

func (s *SDK) discoveryMetadata() PluginMetadata {
	info := PluginMetadata{
		ProtocolVersion:   RPCProtocolVersion,
		Name:              s.Metadata.Name,
		BinaryName:        filepath.Base(os.Args[0]),
		Version:           s.Metadata.Version,
		Description:       s.Metadata.Description,
		Author:            s.Metadata.Author,
		TemplateRepo:      strings.TrimSpace(s.Metadata.TemplateRepo),
		Includes:          append([]string{}, s.Metadata.Includes...),
		CreateDefinitions: s.CreateDefinitions(),
		DeployDefinitions: s.DeployDefinitions(),
		CanDebug:          s.hasDebug,
		CanConverge:       s.hasConverge,
		CanSet:            s.hasSet,
		CanValidate:       s.hasValidate,
		CanHealthcheck:    s.hasHealthcheck,
	}
	info.CanCreate = len(info.CreateDefinitions) > 0
	info.CanDeploy = len(info.DeployDefinitions) > 0
	if info.TemplateRepo == "" {
		if spec, ok := defaultCreateDefinition(info.CreateDefinitions); ok {
			info.TemplateRepo = strings.TrimSpace(spec.DockerComposeRepo)
		}
	}
	return info
}

func (s *SDK) rpcProjectDetect(projectDir string) (RPCResponse, error) {
	claim, err := s.detectOwnProject(projectDir)
	if err != nil {
		return RPCResponse{}, err
	}
	result := projectDetectionResult{Claimed: claim != nil}
	if claim != nil {
		result.Plugin = claim.Plugin
		result.ProjectDir = claim.ProjectDir
		result.Reason = claim.Reason
	}
	return rpcResponse(result, "")
}

func (s *SDK) rpcJobList() (RPCResponse, error) {
	specs := make([]corejob.Spec, 0, len(s.jobs))
	for _, registered := range s.jobs {
		specs = append(specs, registered.Spec)
	}
	return rpcResponse(specs, "")
}

func (s *SDK) rpcComponentCommand(rpcCmd *cobra.Command, name string, req RPCRequest, params ComponentTargetParams) (RPCResponse, error) {
	args := []string{name}
	args, err := appendComponentTargetArgs(s.componentRootCmd, name, args, params)
	if err != nil {
		return RPCResponse{}, fmt.Errorf("build %s argv: %w", req.Method, err)
	}
	args = append(args, req.Args...)
	return s.rpcCommand(rpcCmd, req.Method, s.componentRootCmd, args)
}

func (s *SDK) rpcValidate(rpcCmd *cobra.Command, req RPCRequest, params ValidateRunParams) (RPCResponse, error) {
	if s.validateCmd == nil {
		return rpcResponse([]sitevalidate.Result{}, "")
	}
	var results []sitevalidate.Result
	args, err := flagOnlyRPCArgs(s.validateCmd, params, req.Args)
	if err != nil {
		return RPCResponse{}, fmt.Errorf("build %s argv: %w", req.Method, err)
	}
	output, err := executeRPCCommandWithRunE(rpcCommandContext(rpcCmd), req.Method, s.validateCmd, args, rpcCommandIOFromCommand(rpcCmd), func(cmd *cobra.Command, args []string) error {
		if s.validateRunner == nil {
			results = []sitevalidate.Result{}
			return nil
		}
		var runErr error
		results, runErr = s.runValidateRunner(cmd, s.validateRunner)
		return runErr
	})
	if err != nil {
		return RPCResponse{Output: output}, err
	}
	return rpcResponse(results, output)
}

func (s *SDK) rpcHealthcheck(rpcCmd *cobra.Command, req RPCRequest, params HealthcheckRunParams) (RPCResponse, error) {
	if s.healthcheckCmd == nil {
		return rpcResponse([]sitevalidate.Result{}, "")
	}
	var results []sitevalidate.Result
	args, err := flagOnlyRPCArgs(s.healthcheckCmd, params, req.Args)
	if err != nil {
		return RPCResponse{}, fmt.Errorf("build %s argv: %w", req.Method, err)
	}
	output, err := executeRPCCommandWithRunE(rpcCommandContext(rpcCmd), req.Method, s.healthcheckCmd, args, rpcCommandIOFromCommand(rpcCmd), func(cmd *cobra.Command, args []string) error {
		if s.healthcheckRunner == nil {
			results = []sitevalidate.Result{}
			return nil
		}
		var runErr error
		results, runErr = s.runHealthcheckRunner(cmd, s.healthcheckRunner)
		return runErr
	})
	if err != nil {
		return RPCResponse{Output: output}, err
	}
	return rpcResponse(results, output)
}

func (s *SDK) rpcCommand(rpcCmd *cobra.Command, method string, command *cobra.Command, args []string) (RPCResponse, error) {
	if command == nil {
		return RPCResponse{}, newRPCError(RPCErrorCodeNotRegistered, fmt.Errorf("rpc method is not registered by plugin %q", s.Metadata.Name))
	}
	output, err := executeRPCCommandWithIO(rpcCommandContext(rpcCmd), method, command, args, rpcCommandIOFromCommand(rpcCmd))
	if err != nil {
		return rpcErrorResponseWithOutput(err, output), nil
	}
	return rpcResponse(nil, output)
}

type rpcCommandIO struct {
	stdin  io.Reader
	stderr io.Writer
}

func rpcCommandIOFromCommand(cmd *cobra.Command) rpcCommandIO {
	if cmd == nil {
		return rpcCommandIO{stdin: os.Stdin, stderr: os.Stderr}
	}
	return rpcCommandIO{stdin: cmd.InOrStdin(), stderr: cmd.ErrOrStderr()}
}

func rpcCommandContext(cmd *cobra.Command) context.Context {
	if cmd == nil {
		return context.Background()
	}
	return cmd.Context()
}

func executeRPCCommandWithRunE(runCtx context.Context, method string, command *cobra.Command, args []string, streams rpcCommandIO, runE func(*cobra.Command, []string) error) (string, error) {
	if command == nil {
		return "", fmt.Errorf("rpc command is nil")
	}
	originalRun := command.Run
	originalRunE := command.RunE
	command.Run = nil
	command.RunE = runE
	defer func() {
		command.Run = originalRun
		command.RunE = originalRunE
	}()
	return executeRPCCommandWithIO(runCtx, method, command, args, streams)
}

func executeRPCCommandWithIO(runCtx context.Context, method string, command *cobra.Command, args []string, streams rpcCommandIO) (string, error) {
	if command == nil {
		return "", fmt.Errorf("rpc command is nil")
	}
	stdout := newLimitedRPCBuffer(maxRPCResponseBytes)
	originalContexts := commandContextSnapshot(command)
	originalIn := command.InOrStdin()
	originalOut := command.OutOrStdout()
	originalErr := command.ErrOrStderr()
	originalSilenceUsage := command.SilenceUsage
	defer func() {
		restoreCommandContexts(originalContexts)
		command.SetArgs(nil)
		command.SetIn(originalIn)
		command.SetOut(originalOut)
		command.SetErr(originalErr)
		command.SilenceUsage = originalSilenceUsage
	}()

	setCommandContext(command, runCtx)
	command.SetArgs(args)
	if streams.stdin == nil {
		streams.stdin = os.Stdin
	}
	if streams.stderr == nil {
		streams.stderr = os.Stderr
	}
	command.SetIn(streams.stdin)
	command.SetOut(stdout)
	// Stderr intentionally bypasses the response envelope. The host captures
	// the plugin process stderr separately so progress, prompts, and diagnostics
	// can stream while stdout remains reserved for the JSON response.
	command.SetErr(streams.stderr)
	command.SilenceUsage = true
	err := command.Execute()
	if stdout.Exceeded() {
		return "", rpcOutputLimitError(rpcCommandOutputLimitScope(method, command))
	}
	if err != nil {
		return stdout.String(), err
	}
	return stdout.String(), nil
}

func rpcOutputLimitError(scope string) error {
	scope = strings.TrimSpace(scope)
	if scope == "" {
		scope = "rpc output"
	}
	return fmt.Errorf("%s exceeds %d bytes; reduce output, use --format json when available, or narrow the request", scope, maxRPCResponseBytes)
}

func rpcCommandOutputLimitScope(method string, command *cobra.Command) string {
	parts := []string{}
	if method = strings.TrimSpace(method); method != "" {
		parts = append(parts, fmt.Sprintf("rpc method %s", method))
	}
	if command != nil {
		if path := strings.TrimSpace(command.CommandPath()); path != "" {
			parts = append(parts, fmt.Sprintf("command %q output", path))
		}
	}
	if len(parts) == 0 {
		return "rpc command output"
	}
	return strings.Join(parts, " ")
}

func setCommandContext(command *cobra.Command, runCtx context.Context) {
	if command == nil {
		return
	}
	if runCtx == nil {
		runCtx = context.Background()
	}
	command.SetContext(runCtx)
	for _, child := range command.Commands() {
		setCommandContext(child, runCtx)
	}
}

func commandContextSnapshot(command *cobra.Command) map[*cobra.Command]context.Context {
	values := map[*cobra.Command]context.Context{}
	collectCommandContexts(command, values)
	return values
}

func collectCommandContexts(command *cobra.Command, values map[*cobra.Command]context.Context) {
	if command == nil {
		return
	}
	values[command] = command.Context()
	for _, child := range command.Commands() {
		collectCommandContexts(child, values)
	}
}

func restoreCommandContexts(values map[*cobra.Command]context.Context) {
	for command, runCtx := range values {
		command.SetContext(runCtx)
	}
}

// RegisterComponentCommand registers a hidden component command tree for RPC
// dispatch from core sitectl. Registered describe, reconcile, and set
// subcommands must declare the flags bridged by their typed RPC params;
// registration panics if a required flag is missing.
func (s *SDK) RegisterComponentCommand(cmd *cobra.Command) {
	if s == nil || cmd == nil {
		return
	}
	cmd.Use = "component"
	cmd.Hidden = true
	mustValidateRPCParamFlagsIfCommandExists(MethodComponentDescribe, cmd, "describe", ComponentTargetParams{})
	mustValidateRPCParamFlagsIfCommandExists(MethodComponentReconcile, cmd, "reconcile", ComponentTargetParams{})
	mustValidateRPCParamFlagsIfCommandExists(MethodComponentSet, cmd, "set", ComponentSetParams{})
	s.componentRootCmd = cmd
}

// registerDebugCommand registers the debug command used by RPC dispatch.
// It panics when cmd does not declare the flags bridged by DebugRunParams.
func (s *SDK) registerDebugCommand(cmd *cobra.Command) {
	if s != nil && cmd != nil {
		mustValidateRPCParamFlags(MethodDebugRun, cmd, "", DebugRunParams{})
		s.debugCmd = cmd
	}
}

// registerConvergeCommand registers the converge command used by RPC dispatch.
// It panics when cmd does not declare the flags bridged by ConvergeRunParams.
func (s *SDK) registerConvergeCommand(cmd *cobra.Command) {
	if s != nil && cmd != nil {
		mustValidateRPCParamFlags(MethodConvergeRun, cmd, "", ConvergeRunParams{})
		s.convergeCmd = cmd
	}
}

// registerSetCommand registers the set command used by RPC dispatch.
// It panics when cmd does not declare the flags bridged by SetRunParams.
func (s *SDK) registerSetCommand(cmd *cobra.Command) {
	if s != nil && cmd != nil {
		mustValidateRPCParamFlags(MethodSetRun, cmd, "", SetRunParams{})
		s.setCmd = cmd
	}
}

// registerValidateCommand registers the validate command used by RPC dispatch.
// It panics when cmd does not declare the flags bridged by ValidateRunParams.
func (s *SDK) registerValidateCommand(cmd *cobra.Command) {
	if s != nil && cmd != nil {
		mustValidateRPCParamFlags(MethodValidateRun, cmd, "", ValidateRunParams{})
		s.validateCmd = cmd
	}
}

// registerHealthcheckCommand registers the healthcheck command used by RPC dispatch.
// It panics when cmd does not declare the flags bridged by HealthcheckRunParams.
func (s *SDK) registerHealthcheckCommand(cmd *cobra.Command) {
	if s != nil && cmd != nil {
		mustValidateRPCParamFlags(MethodHealthcheckRun, cmd, "", HealthcheckRunParams{})
		s.healthcheckCmd = cmd
	}
}

func appendComponentTargetArgs(command *cobra.Command, subcommand string, args []string, params ComponentTargetParams) ([]string, error) {
	method := ""
	switch subcommand {
	case "describe":
		method = MethodComponentDescribe
	case "reconcile":
		method = MethodComponentReconcile
	}
	return appendRPCParamFlagsForMethod(method, command, subcommand, args, params)
}

func componentSetArgs(params ComponentSetParams, passthrough []string) ([]string, error) {
	if strings.TrimSpace(params.Name) == "" {
		return nil, fmt.Errorf("component name is required")
	}
	args := []string{"set"}
	args, err := appendRPCParamPositionals(args, params)
	if err != nil {
		return nil, err
	}
	args, err = appendRPCParamFlags(nil, "", args, params)
	if err != nil {
		return nil, err
	}
	return append(args, passthrough...), nil
}

func flagOnlyRPCArgs(command *cobra.Command, params any, passthrough []string) ([]string, error) {
	args, err := appendRPCParamFlags(command, "", nil, params)
	if err != nil {
		return nil, err
	}
	return append(args, passthrough...), nil
}

func rpcCommandTarget(command *cobra.Command, subcommand string) *cobra.Command {
	if command == nil || strings.TrimSpace(subcommand) == "" {
		return command
	}
	name := strings.TrimSpace(subcommand)
	for _, child := range command.Commands() {
		if child.Name() == name || child.HasAlias(name) {
			return child
		}
	}
	return command
}

func rpcCommandHasSubcommand(command *cobra.Command, subcommand string) bool {
	if command == nil {
		return false
	}
	if strings.TrimSpace(subcommand) == "" {
		return true
	}
	name := strings.TrimSpace(subcommand)
	for _, child := range command.Commands() {
		if child.Name() == name || child.HasAlias(name) {
			return true
		}
	}
	return false
}

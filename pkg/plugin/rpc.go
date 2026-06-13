package plugin

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	// RPCProtocolVersion is the host/plugin JSON protocol version.
	//
	// The RPC protocol is intentionally strict lockstep, not negotiated: a
	// missing version (0) is treated as the current version for callers that
	// predate the explicit field, and any non-current explicit version is
	// rejected. Bumping this value requires rebuilding compatible host and
	// plugin binaries together.
	RPCProtocolVersion = 1

	// MethodPluginMetadata requests plugin capability metadata.
	MethodPluginMetadata = "plugin.metadata"
	// MethodProjectDetect asks a plugin to claim a local project directory.
	MethodProjectDetect = "project.detect"
	// MethodCreateRun runs a registered create definition.
	MethodCreateRun = "create.run"
	// MethodCreateComponentDefinitions lists create definitions exposed by components.
	MethodCreateComponentDefinitions = "create.component_definitions"
	// MethodDeployRun runs a registered deploy hook.
	MethodDeployRun = "deploy.run"
	// MethodJobList lists registered plugin jobs.
	MethodJobList = "job.list"
	// MethodJobRun runs one registered plugin job.
	MethodJobRun = "job.run"
	// MethodComponentList lists plugin component definitions.
	MethodComponentList = "component.list"
	// MethodComponentDescribe describes plugin component state.
	MethodComponentDescribe = "component.describe"
	// MethodComponentReconcile reconciles plugin component defaults.
	MethodComponentReconcile = "component.reconcile"
	// MethodComponentSet applies one plugin component state change.
	MethodComponentSet = "component.set"
	// MethodValidateRun runs plugin validators.
	MethodValidateRun = "validate.run"
	// MethodHealthcheckRun runs plugin health checks.
	MethodHealthcheckRun = "healthcheck.run"
	// MethodVerifyRun runs plugin behavioral verification checks.
	MethodVerifyRun = "verify.run"
	// MethodDebugRun renders plugin debug sections.
	MethodDebugRun = "debug.run"
	// MethodSetRun runs the plugin-level set handler.
	MethodSetRun = "set.run"
	// MethodConvergeRun runs the plugin-level converge handler.
	MethodConvergeRun = "converge.run"

	maxRPCRequestBytes  = 4 << 20
	maxRPCResponseBytes = 4 << 20
)

// RPCRequest is the single host-to-plugin request envelope accepted by
// __sitectl-rpc.
//
// Method is a strict core-owned vocabulary. Plugins expose capabilities by
// registering handlers for these methods; adding a new RPC method requires a
// coordinated sitectl and plugin release, not an open-ended plugin extension.
type RPCRequest struct {
	ProtocolVersion int    `json:"protocol_version"`
	Method          string `json:"method"`
	Context         string `json:"context,omitempty"`
	LogLevel        string `json:"log_level,omitempty"`
	// Args is method-specific passthrough argv. Interactive RPC calls that set
	// CommandExecOptions.Stdin send the whole request through argv, so args and
	// params may be visible in process listings. Args are caller-trusted and are
	// not covered by params sensitivity checks; never put secrets in Args.
	Args []string `json:"args,omitempty"`
	// Params is method-specific JSON. Interactive RPC calls that set
	// CommandExecOptions.Stdin send the whole request through argv, so args and
	// params may be visible in process listings. Never put secrets in Params.
	Params json.RawMessage `json:"params,omitempty"`

	paramsSensitive bool
	paramsChecked   bool
}

// RPCResponse is the single plugin-to-host response envelope emitted by
// __sitectl-rpc.
type RPCResponse struct {
	ProtocolVersion int             `json:"protocol_version"`
	OK              bool            `json:"ok"`
	Result          json.RawMessage `json:"result,omitempty"`
	Output          string          `json:"output,omitempty"`
	Error           *RPCError       `json:"error,omitempty"`
}

// RPCError is the structured failure payload returned by plugin RPC handlers.
// Code is an open vocabulary: RPCErrorCode* values are reserved for core
// sitectl protocol and dispatch failures, and plugins may return their own
// domain-specific codes for callers that know how to interpret them.
type RPCError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

const (
	// RPCErrorCodePluginError is the generic code for handler failures.
	RPCErrorCodePluginError = "plugin_error"
	// RPCErrorCodeDecodeRequest means the request envelope could not be decoded.
	RPCErrorCodeDecodeRequest = "decode_request"
	// RPCErrorCodeDecodeParams means method params could not be decoded.
	RPCErrorCodeDecodeParams = "decode_params"
	// RPCErrorCodeUnsupportedMethod means the plugin does not know the method.
	RPCErrorCodeUnsupportedMethod = "unsupported_method"
	// RPCErrorCodeNotRegistered means the plugin did not register that capability.
	RPCErrorCodeNotRegistered = "not_registered"
)

type rpcCodedError struct {
	code string
	err  error
}

func (e *rpcCodedError) Error() string {
	if e == nil || e.err == nil {
		return "unknown error"
	}
	return e.err.Error()
}

func (e *rpcCodedError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

func newRPCError(code string, err error) error {
	if err == nil {
		err = errors.New("unknown error")
	}
	return &rpcCodedError{code: strings.TrimSpace(code), err: err}
}

// RPCFailure is returned by InvokePluginRPC when the plugin returns a valid
// RPC error envelope. Code may be one of the core-reserved RPCErrorCode*
// constants or a plugin-defined value; use IsRPCErrorCode to branch only on
// codes your caller understands.
type RPCFailure struct {
	Plugin  string
	Method  string
	Code    string
	Message string
	Output  string
	Err     error
}

// Error returns the plugin-provided RPC error message.
func (e *RPCFailure) Error() string {
	if e == nil {
		return "unknown rpc failure"
	}
	if strings.TrimSpace(e.Message) != "" {
		return e.Message
	}
	if e.Err != nil {
		return e.Err.Error()
	}
	return fmt.Sprintf("plugin %q rpc method %s failed", e.Plugin, e.Method)
}

// Unwrap returns the wrapped process or protocol error, when one exists.
func (e *RPCFailure) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// IsRPCErrorCode reports whether err is an RPCFailure with the given code.
func IsRPCErrorCode(err error, code string) bool {
	var failure *RPCFailure
	return errors.As(err, &failure) && failure.Code == strings.TrimSpace(code)
}

func newRPCFailure(pluginName, method string, resp RPCResponse, err error) *RPCFailure {
	failure := &RPCFailure{
		Plugin: pluginName,
		Method: method,
		Code:   RPCErrorCodePluginError,
		Output: resp.Output,
		Err:    err,
	}
	if resp.Error != nil {
		failure.Code = strings.TrimSpace(resp.Error.Code)
		failure.Message = strings.TrimSpace(resp.Error.Message)
	}
	if failure.Code == "" {
		failure.Code = RPCErrorCodePluginError
	}
	return failure
}

// ProjectDetectParams is the params payload for MethodProjectDetect.
type ProjectDetectParams struct {
	Path string `json:"path"`
}

// CreateRunParams is the params payload for MethodCreateRun.
type CreateRunParams struct {
	Name string `json:"name"`
}

// DeployRunParams is the params payload for MethodDeployRun.
type DeployRunParams struct {
	Hook string `json:"hook"`
}

// JobRunParams is the params payload for MethodJobRun.
type JobRunParams struct {
	Name string `json:"name"`
}

// ComponentListParams is the params payload for MethodComponentList.
type ComponentListParams struct {
	Name string `json:"name,omitempty"`
}

// ComponentTargetParams is the params payload for component describe and
// reconcile RPC methods.
//
// The rpc_* tags are the host/plugin argv bridge contract. Keep these fields
// in lockstep with serviceComponentRegistry.describeCommand and
// serviceComponentRegistry.reconcileCommand flags; RegisterComponentCommand
// validates the mirrored flag names and value types at plugin startup.
type ComponentTargetParams struct {
	Name           string `json:"name,omitempty" rpc_flags:"component"`
	Path           string `json:"path,omitempty" rpc_flags:"path"`
	CodebaseRootfs string `json:"codebase_rootfs,omitempty" rpc_flags:"codebase-rootfs,drupal-rootfs" rpc_rootfs:"true"`
	Report         bool   `json:"report,omitempty" rpc_flags:"report" rpc_methods:"component.reconcile"`
	Verbose        bool   `json:"verbose,omitempty" rpc_flags:"verbose"`
	Format         string `json:"format,omitempty" rpc_flags:"format"`
	Yolo           bool   `json:"yolo,omitempty" rpc_flags:"yolo" rpc_methods:"component.reconcile"`
}

// ComponentSetParams is the params payload for MethodComponentSet.
//
// The rpc_* tags are the host/plugin argv bridge contract. Keep these fields
// in lockstep with serviceComponentRegistry.setCommand flags and positionals;
// RegisterComponentCommand validates the mirrored flag names and value types at
// plugin startup.
type ComponentSetParams struct {
	Name string `json:"name,omitempty" rpc_pos:"0"`
	// Disposition is the positional disposition/state value and is mutually
	// exclusive with DispositionFlag.
	Disposition string `json:"disposition,omitempty" rpc_pos:"1"`
	Path        string `json:"path,omitempty" rpc_flags:"path"`
	State       string `json:"state,omitempty" rpc_flags:"state"`
	// DispositionFlag is the --disposition value and is mutually exclusive
	// with Disposition.
	DispositionFlag string `json:"disposition_flag,omitempty" rpc_flags:"disposition"`
	Yolo            bool   `json:"yolo,omitempty" rpc_flags:"yolo"`
}

// DebugRunParams is the params payload for MethodDebugRun.
type DebugRunParams struct {
	Verbose bool `json:"verbose,omitempty" rpc_flags:"verbose"`
}

// SetRunParams is the params payload for MethodSetRun.
type SetRunParams struct {
	Path string `json:"path,omitempty" rpc_flags:"path"`
}

// ConvergeRunParams is the params payload for MethodConvergeRun.
type ConvergeRunParams struct {
	Path           string `json:"path,omitempty" rpc_flags:"path"`
	CodebaseRootfs string `json:"codebase_rootfs,omitempty" rpc_flags:"codebase-rootfs,drupal-rootfs" rpc_rootfs:"true"`
	Report         bool   `json:"report,omitempty" rpc_flags:"report"`
	Verbose        bool   `json:"verbose,omitempty" rpc_flags:"verbose"`
	Format         string `json:"format,omitempty" rpc_flags:"format"`
}

// ValidateRunParams is the params payload for MethodValidateRun.
type ValidateRunParams struct {
	CodebaseRootfs string `json:"codebase_rootfs,omitempty" rpc_flags:"codebase-rootfs,drupal-rootfs" rpc_rootfs:"true"`
}

// HealthcheckRunParams is the params payload for MethodHealthcheckRun.
type HealthcheckRunParams struct{}

// VerifyRunParams is the params payload for MethodVerifyRun.
type VerifyRunParams struct{}

// NewRPCRequest creates a request envelope for a plugin RPC method.
func NewRPCRequest(method string) RPCRequest {
	return RPCRequest{
		ProtocolVersion: RPCProtocolVersion,
		Method:          method,
	}
}

// NewProjectDetectRequest creates a typed project.detect request.
func NewProjectDetectRequest(path string) (RPCRequest, error) {
	req := NewRPCRequest(MethodProjectDetect)
	return withRPCParams(req, ProjectDetectParams{Path: path})
}

// NewCreateRunRequest creates a typed create.run request.
func NewCreateRunRequest(name string, args ...string) (RPCRequest, error) {
	req := NewRPCRequest(MethodCreateRun)
	req.Args = copyRPCArgs(args)
	return withRPCParams(req, CreateRunParams{Name: name})
}

// NewDeployRunRequest creates a typed deploy.run request.
func NewDeployRunRequest(hook string, args ...string) (RPCRequest, error) {
	req := NewRPCRequest(MethodDeployRun)
	req.Args = copyRPCArgs(args)
	return withRPCParams(req, DeployRunParams{Hook: hook})
}

// NewJobRunRequest creates a typed job.run request.
func NewJobRunRequest(name string, args ...string) (RPCRequest, error) {
	req := NewRPCRequest(MethodJobRun)
	req.Args = copyRPCArgs(args)
	return withRPCParams(req, JobRunParams{Name: name})
}

// NewComponentListRequest creates a typed component.list request.
func NewComponentListRequest(name string) (RPCRequest, error) {
	req := NewRPCRequest(MethodComponentList)
	return withRPCParams(req, ComponentListParams{Name: name})
}

// NewComponentDescribeRequest creates a typed component.describe request.
func NewComponentDescribeRequest(params ComponentTargetParams, args ...string) (RPCRequest, error) {
	req := NewRPCRequest(MethodComponentDescribe)
	req.Args = copyRPCArgs(args)
	return withRPCParams(req, params)
}

// NewComponentReconcileRequest creates a typed component.reconcile request.
func NewComponentReconcileRequest(params ComponentTargetParams, args ...string) (RPCRequest, error) {
	req := NewRPCRequest(MethodComponentReconcile)
	req.Args = copyRPCArgs(args)
	return withRPCParams(req, params)
}

// NewComponentSetRequest creates a typed component.set request.
func NewComponentSetRequest(params ComponentSetParams, args ...string) (RPCRequest, error) {
	req := NewRPCRequest(MethodComponentSet)
	req.Args = copyRPCArgs(args)
	return withRPCParams(req, params)
}

// NewDebugRunRequest creates a typed debug.run request.
func NewDebugRunRequest(params DebugRunParams, args ...string) (RPCRequest, error) {
	req := NewRPCRequest(MethodDebugRun)
	req.Args = copyRPCArgs(args)
	return withRPCParams(req, params)
}

// NewSetRunRequest creates a typed set.run request.
func NewSetRunRequest(params SetRunParams, args ...string) (RPCRequest, error) {
	req := NewRPCRequest(MethodSetRun)
	req.Args = copyRPCArgs(args)
	return withRPCParams(req, params)
}

// NewConvergeRunRequest creates a typed converge.run request.
func NewConvergeRunRequest(params ConvergeRunParams, args ...string) (RPCRequest, error) {
	req := NewRPCRequest(MethodConvergeRun)
	req.Args = copyRPCArgs(args)
	return withRPCParams(req, params)
}

// NewValidateRunRequest creates a typed validate.run request.
func NewValidateRunRequest(params ValidateRunParams, args ...string) (RPCRequest, error) {
	req := NewRPCRequest(MethodValidateRun)
	req.Args = copyRPCArgs(args)
	return withRPCParams(req, params)
}

// NewHealthcheckRunRequest creates a typed healthcheck.run request.
func NewHealthcheckRunRequest(params HealthcheckRunParams, args ...string) (RPCRequest, error) {
	req := NewRPCRequest(MethodHealthcheckRun)
	req.Args = copyRPCArgs(args)
	return withRPCParams(req, params)
}

// NewVerifyRunRequest creates a typed verify.run request.
func NewVerifyRunRequest(params VerifyRunParams, args ...string) (RPCRequest, error) {
	req := NewRPCRequest(MethodVerifyRun)
	req.Args = copyRPCArgs(args)
	return withRPCParams(req, params)
}

func withRPCParams(req RPCRequest, params any) (RPCRequest, error) {
	if err := validateRPCParamsForMethod(req.Method, params); err != nil {
		return RPCRequest{}, err
	}
	data, err := RPCParams(params)
	if err != nil {
		return RPCRequest{}, err
	}
	req.Params = data
	req.paramsSensitive = rpcParamsAreSensitive(params)
	req.paramsChecked = true
	return req, nil
}

func copyRPCArgs(args []string) []string {
	if len(args) == 0 {
		return nil
	}
	return append([]string{}, args...)
}

func encodeRPCRequestFlag(req RPCRequest) (string, error) {
	req.ProtocolVersion = normalizeRPCProtocolVersion(req.ProtocolVersion)
	data, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("marshal rpc request: %w", err)
	}
	return base64.StdEncoding.EncodeToString(data), nil
}

func decodeRPCRequestFlag(encoded string) (RPCRequest, error) {
	encoded = strings.TrimSpace(encoded)
	if len(encoded) > base64.StdEncoding.EncodedLen(maxRPCRequestBytes) {
		return RPCRequest{}, fmt.Errorf("rpc request exceeds %d bytes", maxRPCRequestBytes)
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return RPCRequest{}, fmt.Errorf("decode rpc request flag: %w", err)
	}
	if len(data) > maxRPCRequestBytes {
		return RPCRequest{}, fmt.Errorf("rpc request exceeds %d bytes", maxRPCRequestBytes)
	}
	return decodeRPCRequest(data)
}

func readRPCRequest(r io.Reader) (RPCRequest, error) {
	limited := &io.LimitedReader{R: r, N: maxRPCRequestBytes + 1}
	data, err := io.ReadAll(limited)
	if err != nil {
		return RPCRequest{}, fmt.Errorf("read rpc request: %w", err)
	}
	if len(data) > maxRPCRequestBytes {
		return RPCRequest{}, fmt.Errorf("rpc request exceeds %d bytes", maxRPCRequestBytes)
	}
	return decodeRPCRequest(data)
}

func decodeRPCRequest(data []byte) (RPCRequest, error) {
	var req RPCRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return RPCRequest{}, fmt.Errorf("parse rpc request: %w", err)
	}
	req.ProtocolVersion = normalizeRPCProtocolVersion(req.ProtocolVersion)
	if req.ProtocolVersion != RPCProtocolVersion {
		return RPCRequest{}, errors.New(rpcProtocolVersionMismatchMessage(req.ProtocolVersion))
	}
	if strings.TrimSpace(req.Method) == "" {
		return RPCRequest{}, fmt.Errorf("rpc method is required")
	}
	return req, nil
}

func rpcProtocolVersionMismatchMessage(version int) string {
	return fmt.Sprintf("unsupported rpc protocol version %d; rebuild or reinstall the plugin to match sitectl (expected version %d)", version, RPCProtocolVersion)
}

func normalizeRPCProtocolVersion(version int) int {
	// Version 0 is a permanent compatibility shim for older plugin binaries
	// that emitted envelopes before protocol_version existed. It is not a
	// negotiation path; explicit non-current versions are still rejected.
	if version == 0 {
		return RPCProtocolVersion
	}
	return version
}

func rpcResponse(result any, output string) (RPCResponse, error) {
	resp := RPCResponse{
		ProtocolVersion: RPCProtocolVersion,
		OK:              true,
		Output:          output,
	}
	if result == nil {
		return resp, nil
	}
	data, err := json.Marshal(result)
	if err != nil {
		return RPCResponse{}, fmt.Errorf("marshal rpc result: %w", err)
	}
	resp.Result = data
	return resp, nil
}

func rpcErrorResponse(err error) RPCResponse {
	return rpcErrorResponseWithOutput(err, "")
}

func rpcErrorResponseWithOutput(err error, output string) RPCResponse {
	message := "unknown error"
	if err != nil {
		message = err.Error()
	}
	code := RPCErrorCodePluginError
	var coded *rpcCodedError
	if errors.As(err, &coded) && strings.TrimSpace(coded.code) != "" {
		code = strings.TrimSpace(coded.code)
	}
	return RPCResponse{
		ProtocolVersion: RPCProtocolVersion,
		OK:              false,
		Output:          output,
		Error: &RPCError{
			Code:    code,
			Message: message,
		},
	}
}

// DecodeRPCParams unmarshals a structured method-specific params payload.
//
// The wire contract is strict: unknown fields and trailing JSON values are
// rejected for both stdin and argv RPC transports.
func DecodeRPCParams[T any](raw json.RawMessage) (T, error) {
	return decodeRPCParamsStrict[T](raw)
}

// RPCParams marshals a typed parameter value into a request payload. Prefer the
// typed New*Request builders when creating requests. Interactive argv
// transport validates manually attached params against the known method schema,
// and rejects params that cannot be proven safe for process listings.
func RPCParams(v any) (json.RawMessage, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("marshal rpc params: %w", err)
	}
	return data, nil
}

func ensureRPCRequestArgvSafe(req RPCRequest) error {
	if req.paramsSensitive {
		return fmt.Errorf("rpc request %s contains sensitive params and cannot use argv transport", req.Method)
	}
	if len(req.Params) == 0 || req.paramsChecked {
		return nil
	}
	sensitive, err := rpcRequestParamsAreSensitive(req)
	if err != nil {
		return fmt.Errorf("rpc request %s params cannot use argv transport: %w", req.Method, err)
	}
	if sensitive {
		return fmt.Errorf("rpc request %s contains sensitive params and cannot use argv transport", req.Method)
	}
	return nil
}

func rpcRequestParamsAreSensitive(req RPCRequest) (bool, error) {
	spec, ok := rpcMethodFor(req.Method)
	if !ok {
		return false, fmt.Errorf("unsupported rpc method %q", req.Method)
	}
	if spec.paramsSensitive == nil {
		if len(req.Params) != 0 {
			return false, fmt.Errorf("%s does not accept params", req.Method)
		}
		return false, nil
	}
	return spec.paramsSensitive(req.Params)
}

func rpcDecodedParamsAreSensitive[T any](raw json.RawMessage) (bool, error) {
	params, err := decodeRPCParamsStrict[T](raw)
	if err != nil {
		return false, err
	}
	return rpcParamsAreSensitive(params), nil
}

func decodeRPCParamsStrict[T any](raw json.RawMessage) (T, error) {
	var out T
	if len(raw) == 0 {
		return out, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&out); err != nil {
		return out, fmt.Errorf("parse rpc params: %w", err)
	}
	var extra struct{}
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			err = fmt.Errorf("unexpected trailing JSON value")
		}
		return out, fmt.Errorf("parse rpc params: %w", err)
	}
	return out, nil
}

type limitedRPCBuffer struct {
	bytes.Buffer
	limit    int
	exceeded bool
}

func newLimitedRPCBuffer(limit int) *limitedRPCBuffer {
	return &limitedRPCBuffer{limit: limit}
}

func (b *limitedRPCBuffer) Write(p []byte) (int, error) {
	// Report the full write as accepted even after the retained prefix reaches
	// limit+1 bytes. The caller checks Exceeded after the subprocess exits.
	written := len(p)
	if b == nil {
		return written, nil
	}
	remaining := b.limit + 1 - b.Len()
	if remaining <= 0 {
		b.exceeded = true
		return written, nil
	}
	if len(p) > remaining {
		b.exceeded = true
		p = p[:remaining]
	}
	_, _ = b.Buffer.Write(p)
	return written, nil
}

func (b *limitedRPCBuffer) Exceeded() bool {
	return b != nil && (b.exceeded || b.Len() > b.limit)
}

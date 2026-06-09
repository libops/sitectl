package plugin

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"

	corecomponent "github.com/libops/sitectl/pkg/component"
	"github.com/libops/sitectl/pkg/config"
	corejob "github.com/libops/sitectl/pkg/job"
	sitevalidate "github.com/libops/sitectl/pkg/validate"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

type rpcContextKey string

const rpcContextTestKey rpcContextKey = "rpc-context-test"

func TestHandleRPCComponentListDispatch(t *testing.T) {
	t.Parallel()

	component := testComposeServiceComponent(t, "fcrepo")
	sdk := &SDK{Metadata: Metadata{Name: "isle"}}
	sdk.RegisterServiceComponents(ServiceComponentRegistryOptions{
		DisplayName: "ISLE",
		Components:  []corecomponent.ComposeServiceComponent{component},
	})

	req := NewRPCRequest(MethodComponentList)
	params, err := RPCParams(ComponentListParams{Name: "fcrepo"})
	if err != nil {
		t.Fatalf("RPCParams() error = %v", err)
	}
	req.Params = params

	resp, err := sdk.handleRPC(&cobra.Command{Use: "rpc"}, req)
	if err != nil {
		t.Fatalf("handleRPC() error = %v", err)
	}
	if !resp.OK {
		t.Fatalf("expected OK response, got %+v", resp)
	}
	if !strings.Contains(resp.Output, "fcrepo") {
		t.Fatalf("expected component list output, got %q", resp.Output)
	}
}

func TestHandleRPCWrapsDecodeErrorsWithMethod(t *testing.T) {
	t.Parallel()

	sdk := &SDK{Metadata: Metadata{Name: "isle"}}
	req := NewRPCRequest(MethodComponentList)
	req.Params = json.RawMessage(`{`)

	_, err := sdk.handleRPC(&cobra.Command{Use: "rpc"}, req)
	if err == nil {
		t.Fatal("expected handleRPC() error")
	}
	if !strings.Contains(err.Error(), "decode component.list params") {
		t.Fatalf("expected method-specific decode context, got %v", err)
	}
	if resp := rpcErrorResponse(err); resp.Error == nil || resp.Error.Code != RPCErrorCodeDecodeParams {
		t.Fatalf("expected decode params error code, got %+v", resp.Error)
	}
}

func TestHandleRPCDoesNotClearConfigWhenRequestOmitsContextAndLogLevel(t *testing.T) {
	t.Parallel()

	sdk := &SDK{
		Metadata: Metadata{Name: "isle"},
		Config: Config{
			Context:  "museum",
			LogLevel: "DEBUG",
		},
	}
	resp, err := sdk.handleRPC(&cobra.Command{Use: "rpc"}, NewRPCRequest(MethodPluginMetadata))
	if err != nil {
		t.Fatalf("handleRPC() error = %v", err)
	}
	if !resp.OK {
		t.Fatalf("expected OK response, got %+v", resp)
	}
	if sdk.Config.Context != "museum" || sdk.Config.LogLevel != "DEBUG" {
		t.Fatalf("expected SDK config preserved, got %+v", sdk.Config)
	}
}

func TestRPCUnknownParamsRejectedByHandlerAndArgvGate(t *testing.T) {
	t.Parallel()

	req := NewRPCRequest(MethodDebugRun)
	req.Params = json.RawMessage(`{"unknown":true}`)

	sdk := &SDK{Metadata: Metadata{Name: "isle"}}
	_, handlerErr := sdk.handleRPC(&cobra.Command{Use: "rpc"}, req)
	if handlerErr == nil {
		t.Fatal("expected handler to reject unknown params field")
	}
	if !strings.Contains(handlerErr.Error(), `unknown field "unknown"`) {
		t.Fatalf("expected unknown field handler error, got %v", handlerErr)
	}

	argvErr := ensureRPCRequestArgvSafe(req)
	if argvErr == nil {
		t.Fatal("expected argv gate to reject unknown params field")
	}
	if !strings.Contains(argvErr.Error(), `unknown field "unknown"`) {
		t.Fatalf("expected unknown field argv error, got %v", argvErr)
	}
}

func TestHandleRPCClassifiesUnsupportedMethod(t *testing.T) {
	t.Parallel()

	sdk := &SDK{Metadata: Metadata{Name: "isle"}}
	req := NewRPCRequest("missing.method")

	_, err := sdk.handleRPC(&cobra.Command{Use: "rpc"}, req)
	if err == nil {
		t.Fatal("expected handleRPC() error")
	}
	if resp := rpcErrorResponse(err); resp.Error == nil || resp.Error.Code != RPCErrorCodeUnsupportedMethod {
		t.Fatalf("expected unsupported method error code, got %+v", resp.Error)
	}
}

func TestEveryMethodConstantIsDispatched(t *testing.T) {
	t.Parallel()

	methods := rpcMethodConstants(t)
	if len(methods) == 0 {
		t.Fatal("expected Method* constants")
	}

	sdk := &SDK{Metadata: Metadata{Name: "isle"}}
	for _, method := range methods {
		t.Run(method, func(t *testing.T) {
			req := NewRPCRequest(method)
			_, err := sdk.handleRPC(&cobra.Command{Use: "rpc"}, req)
			if err == nil {
				return
			}
			if resp := rpcErrorResponse(err); resp.Error != nil && resp.Error.Code == RPCErrorCodeUnsupportedMethod {
				t.Fatalf("method constant %q is not dispatched", method)
			}
		})
	}
}

func TestRPCMethodsDecodeExpectedParamsTypes(t *testing.T) {
	t.Parallel()

	want := map[string]string{
		MethodProjectDetect:      "ProjectDetectParams",
		MethodCreateRun:          "CreateRunParams",
		MethodDeployRun:          "DeployRunParams",
		MethodJobRun:             "JobRunParams",
		MethodComponentList:      "ComponentListParams",
		MethodComponentDescribe:  "ComponentTargetParams",
		MethodComponentReconcile: "ComponentTargetParams",
		MethodComponentSet:       "ComponentSetParams",
		MethodValidateRun:        "ValidateRunParams",
		MethodHealthcheckRun:     "HealthcheckRunParams",
		MethodDebugRun:           "DebugRunParams",
		MethodSetRun:             "SetRunParams",
		MethodConvergeRun:        "ConvergeRunParams",
	}

	got := rpcMethodParamTypes(t)
	for method, wantType := range want {
		if got[method] != wantType {
			t.Fatalf("%s decodes %q params, want %q", method, got[method], wantType)
		}
	}
	for method, gotType := range got {
		if _, ok := want[method]; !ok && gotType != "" {
			t.Fatalf("%s unexpectedly decodes %q params", method, gotType)
		}
	}
}

func TestRPCSensitiveCheckUsesMethodRegistry(t *testing.T) {
	t.Parallel()

	for _, method := range rpcMethodConstants(t) {
		t.Run(method, func(t *testing.T) {
			_, err := rpcRequestParamsAreSensitive(NewRPCRequest(method))
			if err == nil {
				return
			}
			if strings.Contains(err.Error(), "unsupported rpc method") {
				t.Fatalf("method constant %q is missing from rpc method registry", method)
			}
		})
	}
}

func TestRPCProtocolVersionIsStrictLockstepContract(t *testing.T) {
	t.Parallel()

	if RPCProtocolVersion != 1 {
		t.Fatalf("RPCProtocolVersion is lockstep; bumping it requires coordinated host/plugin compatibility work and this test must be updated deliberately")
	}

	_, err := decodeRPCRequest([]byte(`{"protocol_version":2,"method":"debug.run"}`))
	if err == nil {
		t.Fatal("expected explicit non-current protocol version to be rejected")
	}
	if !strings.Contains(err.Error(), "unsupported rpc protocol version 2") {
		t.Fatalf("unexpected protocol error: %v", err)
	}
	if !strings.Contains(err.Error(), "rebuild or reinstall the plugin to match sitectl") {
		t.Fatalf("protocol error missing guidance: %v", err)
	}

	req, err := decodeRPCRequest([]byte(`{"method":"debug.run"}`))
	if err != nil {
		t.Fatalf("expected missing legacy protocol version to be accepted, got %v", err)
	}
	if req.ProtocolVersion != RPCProtocolVersion {
		t.Fatalf("legacy protocol version normalized to %d, want %d", req.ProtocolVersion, RPCProtocolVersion)
	}
}

func TestRPCEnvelopeJSONContract(t *testing.T) {
	t.Parallel()

	assertExactJSONTags(t, reflect.TypeOf(RPCRequest{}), map[string]string{
		"ProtocolVersion": "protocol_version",
		"Method":          "method",
		"Context":         "context,omitempty",
		"LogLevel":        "log_level,omitempty",
		"Args":            "args,omitempty",
		"Params":          "params,omitempty",
	})
	assertExactJSONTags(t, reflect.TypeOf(RPCResponse{}), map[string]string{
		"ProtocolVersion": "protocol_version",
		"OK":              "ok",
		"Result":          "result,omitempty",
		"Output":          "output,omitempty",
		"Error":           "error,omitempty",
	})
	assertExactJSONTags(t, reflect.TypeOf(RPCError{}), map[string]string{
		"Code":    "code",
		"Message": "message",
	})

	request := RPCRequest{
		ProtocolVersion: RPCProtocolVersion,
		Method:          MethodValidateRun,
		Context:         "museum",
		LogLevel:        "DEBUG",
		Args:            []string{"--strict"},
		Params:          json.RawMessage(`{"codebase_rootfs":"app/rootfs"}`),
	}
	assertMarshalJSON(t, request, `{"protocol_version":1,"method":"validate.run","context":"museum","log_level":"DEBUG","args":["--strict"],"params":{"codebase_rootfs":"app/rootfs"}}`)

	response := RPCResponse{
		ProtocolVersion: RPCProtocolVersion,
		OK:              false,
		Result:          json.RawMessage(`{"status":"failed"}`),
		Output:          "partial\n",
		Error:           &RPCError{Code: RPCErrorCodePluginError, Message: "boom"},
	}
	assertMarshalJSON(t, response, `{"protocol_version":1,"ok":false,"result":{"status":"failed"},"output":"partial\n","error":{"code":"plugin_error","message":"boom"}}`)

	var roundTrip RPCResponse
	if err := json.Unmarshal([]byte(`{"protocol_version":1,"ok":false,"result":{"status":"failed"},"output":"partial\n","error":{"code":"plugin_error","message":"boom"}}`), &roundTrip); err != nil {
		t.Fatalf("Unmarshal(RPCResponse) error = %v", err)
	}
	if roundTrip.ProtocolVersion != RPCProtocolVersion || roundTrip.OK || roundTrip.Output != "partial\n" || roundTrip.Error == nil || roundTrip.Error.Code != RPCErrorCodePluginError || string(roundTrip.Result) != `{"status":"failed"}` {
		t.Fatalf("unexpected RPCResponse round trip: %+v result=%s", roundTrip, roundTrip.Result)
	}
}

func TestReadRPCRequestRejectsOversizedInput(t *testing.T) {
	t.Parallel()

	_, err := readRPCRequest(strings.NewReader(strings.Repeat("x", maxRPCRequestBytes+1)))
	if err == nil {
		t.Fatal("expected oversized request error")
	}
	if !strings.Contains(err.Error(), "rpc request exceeds") {
		t.Fatalf("unexpected oversized request error: %v", err)
	}
}

func TestDecodeRPCRequestFlagRejectsOversizedInput(t *testing.T) {
	t.Parallel()

	encoded := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("x", maxRPCRequestBytes+1)))
	_, err := decodeRPCRequestFlag(encoded)
	if err == nil {
		t.Fatal("expected oversized request flag error")
	}
	if !strings.Contains(err.Error(), "rpc request exceeds") {
		t.Fatalf("unexpected oversized request flag error: %v", err)
	}
}

func TestGetRPCCommandExecutesStdinRequest(t *testing.T) {
	sdk := NewSDK(Metadata{Name: "isle", Version: "1.2.3", Description: "ISLE"})
	sdk.RegisterDebugRunner(debugRunnerStub{})
	req := NewRPCRequest(MethodPluginMetadata)
	requestData, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal(RPCRequest) error = %v", err)
	}

	cmd := sdk.GetRPCCommand()
	var stdout bytes.Buffer
	cmd.SetIn(bytes.NewReader(requestData))
	cmd.SetOut(&stdout)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.HasSuffix(stdout.String(), "\n") {
		t.Fatalf("expected newline-framed response, got %q", stdout.String())
	}

	resp := decodeRPCCommandResponse(t, stdout.Bytes())
	if !resp.OK || resp.ProtocolVersion != RPCProtocolVersion {
		t.Fatalf("unexpected RPC response: %+v", resp)
	}
	metadata, err := DecodeRPCResult[InstalledPlugin](resp)
	if err != nil {
		t.Fatalf("DecodeRPCResult() error = %v", err)
	}
	if metadata.Name != "isle" || metadata.Version != "1.2.3" || !metadata.CanDebug {
		t.Fatalf("unexpected metadata: %+v", metadata)
	}
}

func TestGetRPCCommandExecutesRequestFlagBeforeStdin(t *testing.T) {
	sdk := NewSDK(Metadata{Name: "flagged"})
	req := NewRPCRequest(MethodPluginMetadata)
	encoded, err := encodeRPCRequestFlag(req)
	if err != nil {
		t.Fatalf("encodeRPCRequestFlag() error = %v", err)
	}

	cmd := sdk.GetRPCCommand()
	var stdout bytes.Buffer
	cmd.SetArgs([]string{"--request", encoded})
	cmd.SetIn(strings.NewReader("{"))
	cmd.SetOut(&stdout)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	resp := decodeRPCCommandResponse(t, stdout.Bytes())
	metadata, err := DecodeRPCResult[InstalledPlugin](resp)
	if err != nil {
		t.Fatalf("DecodeRPCResult() error = %v", err)
	}
	if !resp.OK || metadata.Name != "flagged" {
		t.Fatalf("expected --request payload to win over stdin, resp=%+v metadata=%+v", resp, metadata)
	}
}

func TestGetRPCCommandReturnsDecodeErrorEnvelope(t *testing.T) {
	sdk := NewSDK(Metadata{Name: "isle"})
	cmd := sdk.GetRPCCommand()
	var stdout bytes.Buffer
	cmd.SetIn(strings.NewReader("{"))
	cmd.SetOut(&stdout)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	resp := decodeRPCCommandResponse(t, stdout.Bytes())
	if resp.OK {
		t.Fatalf("expected decode failure response, got %+v", resp)
	}
	if resp.ProtocolVersion != RPCProtocolVersion {
		t.Fatalf("protocol version = %d, want %d", resp.ProtocolVersion, RPCProtocolVersion)
	}
	if resp.Error == nil || resp.Error.Code != RPCErrorCodeDecodeRequest {
		t.Fatalf("expected decode request error, got %+v", resp.Error)
	}
}

func TestGetRPCCommandRejectsReuse(t *testing.T) {
	sdk := NewSDK(Metadata{Name: "isle"})
	req := NewRPCRequest(MethodPluginMetadata)
	requestData, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal(RPCRequest) error = %v", err)
	}

	cmd := sdk.GetRPCCommand()
	var first bytes.Buffer
	cmd.SetIn(bytes.NewReader(requestData))
	cmd.SetOut(&first)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("first Execute() error = %v", err)
	}
	if resp := decodeRPCCommandResponse(t, first.Bytes()); !resp.OK {
		t.Fatalf("expected first response OK, got %+v", resp)
	}

	var second bytes.Buffer
	cmd.SetIn(bytes.NewReader(requestData))
	cmd.SetOut(&second)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("second Execute() error = %v", err)
	}
	resp := decodeRPCCommandResponse(t, second.Bytes())
	if resp.OK {
		t.Fatalf("expected reuse error response, got %+v", resp)
	}
	if resp.Error == nil || !strings.Contains(resp.Error.Message, "already handled a request") {
		t.Fatalf("unexpected reuse error response: %+v", resp)
	}
}

func TestGetRPCCommandReturnsHandlerErrorEnvelopeWithOutput(t *testing.T) {
	sdk := &SDK{Metadata: Metadata{Name: "isle"}}
	sdk.debugCmd = &cobra.Command{
		Use: "debug",
		RunE: func(cmd *cobra.Command, args []string) error {
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "partial output")
			return fmt.Errorf("debug failed")
		},
	}
	req := NewRPCRequest(MethodDebugRun)
	requestData, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal(RPCRequest) error = %v", err)
	}

	cmd := sdk.GetRPCCommand()
	var stdout bytes.Buffer
	cmd.SetIn(bytes.NewReader(requestData))
	cmd.SetOut(&stdout)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	resp := decodeRPCCommandResponse(t, stdout.Bytes())
	if resp.OK {
		t.Fatalf("expected handler failure response, got %+v", resp)
	}
	if resp.Error == nil || resp.Error.Code != RPCErrorCodePluginError {
		t.Fatalf("expected plugin error, got %+v", resp.Error)
	}
	if resp.Output != "partial output\n" {
		t.Fatalf("output = %q, want partial output", resp.Output)
	}
}

func TestGetRPCCommandRejectsOversizedHandlerOutput(t *testing.T) {
	sdk := &SDK{Metadata: Metadata{Name: "isle"}}
	sdk.debugCmd = &cobra.Command{
		Use: "debug",
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := cmd.OutOrStdout().Write([]byte(strings.Repeat("x", maxRPCResponseBytes+1)))
			return err
		},
	}
	req := NewRPCRequest(MethodDebugRun)
	requestData, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal(RPCRequest) error = %v", err)
	}

	cmd := sdk.GetRPCCommand()
	var stdout bytes.Buffer
	cmd.SetIn(bytes.NewReader(requestData))
	cmd.SetOut(&stdout)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	resp := decodeRPCCommandResponse(t, stdout.Bytes())
	if resp.OK {
		t.Fatalf("expected oversized output failure, got %+v", resp)
	}
	if resp.Output != "" {
		t.Fatalf("expected oversized output to be omitted, got %d bytes", len(resp.Output))
	}
	if resp.Error == nil || !strings.Contains(resp.Error.Message, fmt.Sprintf("exceeds %d bytes", maxRPCResponseBytes)) {
		t.Fatalf("expected output limit error, got %+v", resp.Error)
	}
	if !strings.Contains(resp.Error.Message, MethodDebugRun) || !strings.Contains(resp.Error.Message, "use --format json") || !strings.Contains(resp.Error.Message, "narrow") {
		t.Fatalf("expected actionable output limit guidance, got %+v", resp.Error)
	}
	if stdout.Len() > maxRPCResponseBytes {
		t.Fatalf("expected compact error envelope, got %d bytes", stdout.Len())
	}
}

func TestGetRPCCommandExecutesValidateRunTypedResult(t *testing.T) {
	saveValidateTestContext(t)

	runner := &validateRunnerStub{
		codebaseRootfs:   "",
		wantContextValue: "validate-context",
		stdout:           "validate diagnostic",
	}
	sdk := NewSDK(Metadata{Name: "isle"})
	sdk.Config.Context = "museum"
	sdk.RegisterValidateRunner(runner)

	req, err := NewValidateRunRequest(ValidateRunParams{CodebaseRootfs: "app/rootfs"})
	if err != nil {
		t.Fatalf("NewValidateRunRequest() error = %v", err)
	}
	requestData, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal(RPCRequest) error = %v", err)
	}

	cmd := sdk.GetRPCCommand()
	cmd.SetContext(context.WithValue(context.Background(), rpcContextTestKey, "validate-context"))
	var stdout bytes.Buffer
	cmd.SetIn(bytes.NewReader(requestData))
	cmd.SetOut(&stdout)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	resp := decodeRPCCommandResponse(t, stdout.Bytes())
	if !resp.OK {
		t.Fatalf("expected OK response, got %+v", resp)
	}
	if resp.Output != "validate diagnostic\n" {
		t.Fatalf("expected validate stdout to be captured as output, got %q", resp.Output)
	}
	got, err := DecodeRPCResult[[]sitevalidate.Result](resp)
	if err != nil {
		t.Fatalf("DecodeRPCResult() error = %v", err)
	}
	want := []sitevalidate.Result{{
		Name:   "codebase-rootfs",
		Status: sitevalidate.StatusOK,
		Detail: "app/rootfs",
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("validate results = %+v, want %+v", got, want)
	}
}

func TestRPCRequestBuildersEncodeTypedParams(t *testing.T) {
	t.Parallel()

	req, err := NewComponentDescribeRequest(ComponentTargetParams{
		Name:           "fcrepo",
		Path:           "/srv/site",
		CodebaseRootfs: "app/rootfs",
		Verbose:        true,
		Format:         "json",
	})
	if err != nil {
		t.Fatalf("NewComponentDescribeRequest() error = %v", err)
	}

	params, err := DecodeRPCParams[ComponentTargetParams](req.Params)
	if err != nil {
		t.Fatalf("DecodeRPCParams() error = %v", err)
	}
	want := ComponentTargetParams{
		Name:           "fcrepo",
		Path:           "/srv/site",
		CodebaseRootfs: "app/rootfs",
		Verbose:        true,
		Format:         "json",
	}
	if !reflect.DeepEqual(params, want) {
		t.Fatalf("params = %+v, want %+v", params, want)
	}
	if strings.Contains(string(req.Params), "drupal_rootfs") {
		t.Fatalf("expected generic codebase_rootfs contract, got %s", req.Params)
	}
	if !strings.Contains(string(req.Params), "codebase_rootfs") {
		t.Fatalf("expected codebase_rootfs in params, got %s", req.Params)
	}
}

func TestRPCRequestBuildersCoverParamMethods(t *testing.T) {
	t.Parallel()

	builders := map[string]RPCRequest{
		MethodProjectDetect:      mustRPCRequest(NewProjectDetectRequest("/srv/site")),
		MethodCreateRun:          mustRPCRequest(NewCreateRunRequest("default", "--flag")),
		MethodDeployRun:          mustRPCRequest(NewDeployRunRequest("pre-down", "--flag")),
		MethodJobRun:             mustRPCRequest(NewJobRunRequest("sync", "--flag")),
		MethodComponentList:      mustRPCRequest(NewComponentListRequest("fcrepo")),
		MethodComponentDescribe:  mustRPCRequest(NewComponentDescribeRequest(ComponentTargetParams{Name: "fcrepo"}, "--flag")),
		MethodComponentReconcile: mustRPCRequest(NewComponentReconcileRequest(ComponentTargetParams{Name: "fcrepo"}, "--flag")),
		MethodComponentSet:       mustRPCRequest(NewComponentSetRequest(ComponentSetParams{Name: "fcrepo", Disposition: "off"}, "--flag")),
		MethodValidateRun:        mustRPCRequest(NewValidateRunRequest(ValidateRunParams{CodebaseRootfs: "app/rootfs"}, "--flag")),
		MethodHealthcheckRun:     mustRPCRequest(NewHealthcheckRunRequest(HealthcheckRunParams{}, "--flag")),
		MethodDebugRun:           mustRPCRequest(NewDebugRunRequest(DebugRunParams{Verbose: true}, "--flag")),
		MethodSetRun:             mustRPCRequest(NewSetRunRequest(SetRunParams{Path: "/srv/site"}, "--flag")),
		MethodConvergeRun:        mustRPCRequest(NewConvergeRunRequest(ConvergeRunParams{Path: "/srv/site"}, "--flag")),
	}

	for method, spec := range rpcMethodRegistry() {
		if spec.paramsType == nil {
			continue
		}
		if _, ok := builders[method]; !ok {
			t.Fatalf("%s has params type %s but no typed New*Request builder in this contract test", method, spec.paramsType.Name())
		}
	}
	for method, req := range builders {
		spec, ok := rpcMethodFor(method)
		if !ok {
			t.Fatalf("%s has a typed builder but is not registered", method)
		}
		if spec.paramsType == nil {
			t.Fatalf("%s has a typed builder but does not accept params", method)
		}
		if req.Method != method {
			t.Fatalf("%s builder returned method %q", method, req.Method)
		}
		if len(req.Params) == 0 {
			t.Fatalf("%s builder returned empty params", method)
		}
		if _, err := spec.paramsSensitive(req.Params); err != nil {
			t.Fatalf("%s builder params do not decode as %s: %v", method, spec.paramsType.Name(), err)
		}
	}
}

func TestRPCResultsUseSnakeCaseJSONContract(t *testing.T) {
	t.Parallel()

	resultTypes := []reflect.Type{
		reflect.TypeOf(PluginMetadata{}),
		reflect.TypeOf(CreateSpec{}),
		reflect.TypeOf(DeploySpec{}),
		reflect.TypeOf(corejob.Spec{}),
		reflect.TypeOf(sitevalidate.Result{}),
		reflect.TypeOf(projectDetectionResult{}),
		reflect.TypeOf(corecomponent.Definition{}),
	}
	assertJSONTagsUseLowerSnakeCase(t, resultTypes)

	sdk := NewSDK(Metadata{Name: "isle", Description: "ISLE stack"})
	sdk.RegisterCreateRunner(CreateSpec{
		Name:              "default",
		Description:       "Create an ISLE stack",
		Default:           true,
		DockerComposeRepo: "https://github.com/example/isle",
	}, createRunnerStub{})

	metadataResp, err := sdk.handleRPC(&cobra.Command{Use: "rpc"}, NewRPCRequest(MethodPluginMetadata))
	if err != nil {
		t.Fatalf("handleRPC(plugin.metadata) error = %v", err)
	}
	metadata := string(metadataResp.Result)
	if strings.Contains(metadata, "ProtocolVersion") || strings.Contains(metadata, "CreateDefinitions") || strings.Contains(metadata, "DockerComposeRepo") {
		t.Fatalf("metadata result used legacy field name: %s", metadata)
	}
	if !strings.Contains(metadata, `"protocol_version":1`) || !strings.Contains(metadata, `"create_definitions"`) || !strings.Contains(metadata, `"docker_compose_repo"`) {
		t.Fatalf("metadata result missing expected snake_case fields: %s", metadata)
	}

	component := testComposeServiceComponent(t, "fcrepo")
	componentSDK := NewSDK(Metadata{Name: "isle"})
	componentSDK.RegisterServiceComponents(ServiceComponentRegistryOptions{
		DisplayName: "ISLE",
		Components:  []corecomponent.ComposeServiceComponent{component},
	})
	defResp, err := componentSDK.handleRPC(&cobra.Command{Use: "rpc"}, NewRPCRequest(MethodCreateComponentDefinitions))
	if err != nil {
		t.Fatalf("handleRPC(create.component_definitions) error = %v", err)
	}
	defs := string(defResp.Result)
	if !strings.Contains(defs, `"default_state"`) {
		t.Fatalf("component definition result missing default_state: %s", defs)
	}
	if strings.Contains(defs, "DefaultState") {
		t.Fatalf("component definition result used legacy field name: %s", defs)
	}
}

func TestAppendUniqueComponentDefinitionsPrefersExisting(t *testing.T) {
	t.Parallel()

	existing := []corecomponent.Definition{
		{Name: "mariadb", DefaultState: corecomponent.StateOn},
	}
	incoming := []corecomponent.Definition{
		{Name: "mariadb", DefaultState: corecomponent.StateOff},
		{Name: "traefik", DefaultState: corecomponent.StateOn},
	}

	got := appendUniqueComponentDefinitions(existing, incoming)
	if len(got) != 2 {
		t.Fatalf("expected 2 component definitions, got %d: %#v", len(got), got)
	}
	if got[0].Name != "mariadb" || got[0].DefaultState != corecomponent.StateOn {
		t.Fatalf("expected existing mariadb definition to win, got %#v", got[0])
	}
	if got[1].Name != "traefik" {
		t.Fatalf("expected unique included definition appended, got %#v", got[1])
	}
}

func TestDiscoveryMetadataAdvertisesRegisteredCapabilities(t *testing.T) {
	metadata := fullPluginMetadataForTest()
	metadata.CreateDefinitions[0].Plugin = metadata.Name
	oldArgs := os.Args
	os.Args = []string{metadata.BinaryName}
	t.Cleanup(func() {
		os.Args = oldArgs
	})

	sdk := NewSDK(Metadata{
		Name:         metadata.Name,
		Version:      metadata.Version,
		Description:  metadata.Description,
		Author:       metadata.Author,
		TemplateRepo: metadata.TemplateRepo,
		Includes:     metadata.Includes,
	})
	sdk.RegisterCreateRunner(metadata.CreateDefinitions[0], createRunnerStub{})
	sdk.RegisterDeployRunner(metadata.DeployDefinitions[0], deployRunnerStub{})
	sdk.RegisterDebugRunner(debugRunnerStub{})
	sdk.RegisterSetRunner(setBridgeValidationRunner{})
	sdk.RegisterConvergeRunner(convergeBridgeValidationRunner{})
	sdk.RegisterValidateRunner(&validateRunnerStub{})
	sdk.RegisterHealthcheckRunner(&healthcheckRunnerStub{})

	resp, err := sdk.handleRPC(&cobra.Command{Use: "rpc"}, NewRPCRequest(MethodPluginMetadata))
	if err != nil {
		t.Fatalf("handleRPC(plugin.metadata) error = %v", err)
	}
	if !strings.Contains(string(resp.Result), `"can_debug":true`) {
		t.Fatalf("metadata result missing can_debug: %s", resp.Result)
	}
	got, err := DecodeRPCResult[PluginMetadata](resp)
	if err != nil {
		t.Fatalf("DecodeRPCResult() error = %v", err)
	}
	if !got.CanCreate || !got.CanDeploy || !got.CanDebug || !got.CanConverge || !got.CanSet || !got.CanValidate || !got.CanHealthcheck {
		t.Fatalf("expected all registered capabilities to be advertised, got %+v", got)
	}
	if !reflect.DeepEqual(got, metadata) {
		t.Fatalf("discoveryMetadata() = %#v, want %#v", got, metadata)
	}
}

func TestRPCParamsUseSnakeCaseJSONContract(t *testing.T) {
	t.Parallel()

	paramTypes := []reflect.Type{
		reflect.TypeOf(ProjectDetectParams{}),
		reflect.TypeOf(CreateRunParams{}),
		reflect.TypeOf(DeployRunParams{}),
		reflect.TypeOf(JobRunParams{}),
		reflect.TypeOf(ComponentListParams{}),
		reflect.TypeOf(ComponentTargetParams{}),
		reflect.TypeOf(ComponentSetParams{}),
		reflect.TypeOf(DebugRunParams{}),
		reflect.TypeOf(SetRunParams{}),
		reflect.TypeOf(ConvergeRunParams{}),
		reflect.TypeOf(ValidateRunParams{}),
		reflect.TypeOf(HealthcheckRunParams{}),
	}
	assertRPCParamTypeListComplete(t, paramTypes)

	assertJSONTagsUseLowerSnakeCase(t, paramTypes)
}

func TestRPCArgBridgeContractCoversEveryField(t *testing.T) {
	t.Parallel()

	bridgeTypes := []reflect.Type{
		reflect.TypeOf(ComponentTargetParams{}),
		reflect.TypeOf(ComponentSetParams{}),
		reflect.TypeOf(DebugRunParams{}),
		reflect.TypeOf(SetRunParams{}),
		reflect.TypeOf(ConvergeRunParams{}),
		reflect.TypeOf(ValidateRunParams{}),
		reflect.TypeOf(HealthcheckRunParams{}),
	}
	for _, typ := range bridgeTypes {
		t.Run(typ.Name(), func(t *testing.T) {
			if _, _, err := rpcParamValueAndSpecs(reflect.New(typ).Elem().Interface()); err != nil {
				t.Fatalf("rpcParamValueAndSpecs(%s) error = %v", typ.Name(), err)
			}
			for i := 0; i < typ.NumField(); i++ {
				field := typ.Field(i)
				if !field.IsExported() {
					continue
				}
				if field.Tag.Get(rpcFlagsTag) == "" && field.Tag.Get(rpcPositionTag) == "" {
					t.Fatalf("%s.%s is missing RPC argv bridge metadata", typ.Name(), field.Name)
				}
			}
		})
	}
}

func TestRegisteredRPCCommandsDeclareBridgedFlags(t *testing.T) {
	sdk := NewSDK(Metadata{Name: "isle"})
	sdk.RegisterDebugRunner(debugRunnerStub{})
	sdk.RegisterSetRunner(setBridgeValidationRunner{})
	sdk.RegisterConvergeRunner(convergeBridgeValidationRunner{})
	sdk.RegisterValidateRunner(&validateRunnerStub{})
	sdk.RegisterHealthcheckRunner(&healthcheckRunnerStub{})
	sdk.RegisterServiceComponents(ServiceComponentRegistryOptions{
		DisplayName: "ISLE",
		Components:  []corecomponent.ComposeServiceComponent{testComposeServiceComponent(t, "fcrepo")},
	})

	tests := []struct {
		name       string
		method     string
		command    *cobra.Command
		subcommand string
		params     any
	}{
		{name: "debug", method: MethodDebugRun, command: sdk.debugCmd, params: DebugRunParams{}},
		{name: "set", method: MethodSetRun, command: sdk.setCmd, params: SetRunParams{}},
		{name: "converge", method: MethodConvergeRun, command: sdk.convergeCmd, params: ConvergeRunParams{}},
		{name: "validate", method: MethodValidateRun, command: sdk.validateCmd, params: ValidateRunParams{}},
		{name: "healthcheck", method: MethodHealthcheckRun, command: sdk.healthcheckCmd, params: HealthcheckRunParams{}},
		{name: "component describe", method: MethodComponentDescribe, command: sdk.componentRootCmd, subcommand: "describe", params: ComponentTargetParams{}},
		{name: "component reconcile", method: MethodComponentReconcile, command: sdk.componentRootCmd, subcommand: "reconcile", params: ComponentTargetParams{}},
		{name: "component set", method: MethodComponentSet, command: sdk.componentRootCmd, subcommand: "set", params: ComponentSetParams{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := validateRPCParamFlags(tt.method, tt.command, tt.subcommand, tt.params); err != nil {
				t.Fatalf("validateRPCParamFlags() error = %v", err)
			}
		})
	}
}

func TestValidateRPCParamFlagsRejectsFlagTypeMismatch(t *testing.T) {
	tests := []struct {
		name    string
		method  string
		command *cobra.Command
		params  any
		wantErr string
	}{
		{
			name:   "string param bound to bool flag",
			method: MethodSetRun,
			command: func() *cobra.Command {
				cmd := &cobra.Command{Use: "set"}
				cmd.Flags().Bool("path", false, "")
				return cmd
			}(),
			params:  SetRunParams{},
			wantErr: "--path must be a string flag for SetRunParams.Path, got bool",
		},
		{
			name:   "bool param bound to string flag",
			method: MethodDebugRun,
			command: func() *cobra.Command {
				cmd := &cobra.Command{Use: "debug"}
				cmd.Flags().String("verbose", "", "")
				return cmd
			}(),
			params:  DebugRunParams{},
			wantErr: "--verbose must be a bool flag for DebugRunParams.Verbose, got string",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateRPCParamFlags(tt.method, tt.command, "", tt.params)
			if err == nil {
				t.Fatal("expected flag type error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want substring %q", err, tt.wantErr)
			}
		})
	}
}

func TestRegisterRunnerPanicsWhenRPCBridgeFlagMissing(t *testing.T) {
	sdk := NewSDK(Metadata{Name: "isle"})
	defer func() {
		recovered := recover()
		if recovered == nil {
			t.Fatal("expected registration panic")
		}
		if !strings.Contains(fmt.Sprint(recovered), "debug.run") || !strings.Contains(fmt.Sprint(recovered), "--verbose") {
			t.Fatalf("unexpected panic: %v", recovered)
		}
	}()

	sdk.RegisterDebugRunner(debugRunnerWithoutFlags{})
}

func TestValidateRunReturnsTypedJSONResult(t *testing.T) {
	saveValidateTestContext(t)

	runner := &validateRunnerStub{wantContextValue: "validate-context"}
	sdk := NewSDK(Metadata{Name: "isle"})
	sdk.Config.Context = "museum"
	sdk.RegisterValidateRunner(runner)

	req, err := NewValidateRunRequest(ValidateRunParams{CodebaseRootfs: "app/rootfs"})
	if err != nil {
		t.Fatalf("NewValidateRunRequest() error = %v", err)
	}
	rpcCmd := &cobra.Command{Use: "rpc"}
	rpcCmd.SetContext(context.WithValue(context.Background(), rpcContextTestKey, "validate-context"))
	resp, err := sdk.handleRPC(rpcCmd, req)
	if err != nil {
		t.Fatalf("handleRPC() error = %v", err)
	}
	if strings.TrimSpace(resp.Output) != "" {
		t.Fatalf("expected validate.run to use result, got output %q", resp.Output)
	}
	got, err := DecodeRPCResult[[]sitevalidate.Result](resp)
	if err != nil {
		t.Fatalf("DecodeRPCResult() error = %v", err)
	}
	want := []sitevalidate.Result{{
		Name:   "codebase-rootfs",
		Status: sitevalidate.StatusOK,
		Detail: "app/rootfs",
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("validate results = %+v, want %+v", got, want)
	}
}

func TestValidateRunExecutesCobraLifecycle(t *testing.T) {
	saveValidateTestContext(t)

	preRunCalled := false
	runner := &validateLifecycleRunner{preRunCalled: &preRunCalled}
	sdk := NewSDK(Metadata{Name: "isle"})
	sdk.Config.Context = "museum"
	sdk.RegisterValidateRunner(runner)

	req, err := NewValidateRunRequest(ValidateRunParams{})
	if err != nil {
		t.Fatalf("NewValidateRunRequest() error = %v", err)
	}
	resp, err := sdk.handleRPC(&cobra.Command{Use: "rpc"}, req)
	if err != nil {
		t.Fatalf("handleRPC() error = %v", err)
	}
	if !resp.OK {
		t.Fatalf("expected OK response, got %+v", resp)
	}
	if !preRunCalled {
		t.Fatal("expected validate PreRunE to execute")
	}
}

func TestRPCDispatchPromotesHostControlledFlagsAndPreservesPassthroughArgs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		req       RPCRequest
		configure func(*SDK, *[]string, *map[string]string)
		wantArgs  []string
		wantFlags map[string]string
	}{
		{
			name: "debug.run",
			req:  mustRPCRequest(NewDebugRunRequest(DebugRunParams{Verbose: true}, "--plugin-flag", "x")),
			configure: func(sdk *SDK, gotArgs *[]string, gotFlags *map[string]string) {
				cmd := captureCommand("debug", gotArgs, gotFlags)
				cmd.Flags().Bool("verbose", false, "")
				cmd.Flags().String("plugin-flag", "", "")
				sdk.debugCmd = cmd
			},
			wantArgs:  []string{},
			wantFlags: map[string]string{"verbose": "true", "plugin-flag": "x"},
		},
		{
			name: "set.run",
			req:  mustRPCRequest(NewSetRunRequest(SetRunParams{Path: "/srv/site"}, "fcrepo", "off", "--plugin-flag", "x")),
			configure: func(sdk *SDK, gotArgs *[]string, gotFlags *map[string]string) {
				cmd := captureCommand("set", gotArgs, gotFlags)
				cmd.Flags().String("path", "", "")
				cmd.Flags().String("plugin-flag", "", "")
				sdk.setCmd = cmd
			},
			wantArgs:  []string{"fcrepo", "off"},
			wantFlags: map[string]string{"path": "/srv/site", "plugin-flag": "x"},
		},
		{
			name: "converge.run",
			req: mustRPCRequest(NewConvergeRunRequest(ConvergeRunParams{
				Path:           "/srv/site",
				CodebaseRootfs: "app/rootfs",
				Report:         true,
				Verbose:        true,
				Format:         "json",
			}, "--component", "fcrepo")),
			configure: func(sdk *SDK, gotArgs *[]string, gotFlags *map[string]string) {
				cmd := captureCommand("converge", gotArgs, gotFlags)
				cmd.Flags().String("path", "", "")
				cmd.Flags().String("codebase-rootfs", "", "")
				cmd.Flags().Bool("report", false, "")
				cmd.Flags().Bool("verbose", false, "")
				cmd.Flags().String("format", "", "")
				cmd.Flags().String("component", "", "")
				sdk.convergeCmd = cmd
			},
			wantFlags: map[string]string{
				"path":            "/srv/site",
				"codebase-rootfs": "app/rootfs",
				"report":          "true",
				"verbose":         "true",
				"format":          "json",
				"component":       "fcrepo",
			},
			wantArgs: []string{},
		},
		{
			name: "component.set",
			req: mustRPCRequest(NewComponentSetRequest(ComponentSetParams{
				Name:        "fcrepo",
				Disposition: "off",
				Path:        "/srv/site",
				Yolo:        true,
			}, "--tls-mode", "letsencrypt")),
			configure: func(sdk *SDK, gotArgs *[]string, gotFlags *map[string]string) {
				root := &cobra.Command{Use: "component"}
				set := captureCommand("set", gotArgs, gotFlags)
				set.Flags().String("path", "", "")
				set.Flags().Bool("yolo", false, "")
				set.Flags().String("tls-mode", "", "")
				root.AddCommand(set)
				sdk.componentRootCmd = root
			},
			wantArgs:  []string{"fcrepo", "off"},
			wantFlags: map[string]string{"path": "/srv/site", "yolo": "true", "tls-mode": "letsencrypt"},
		},
		{
			name: "component.describe",
			req: mustRPCRequest(NewComponentDescribeRequest(ComponentTargetParams{
				Name:           "fcrepo",
				CodebaseRootfs: "app/rootfs",
			})),
			configure: func(sdk *SDK, gotArgs *[]string, gotFlags *map[string]string) {
				root := &cobra.Command{Use: "component"}
				describe := captureCommand("describe", gotArgs, gotFlags)
				describe.Flags().String("component", "", "")
				describe.Flags().String("codebase-rootfs", "", "")
				root.AddCommand(describe)
				sdk.componentRootCmd = root
			},
			wantArgs:  []string{},
			wantFlags: map[string]string{"component": "fcrepo", "codebase-rootfs": "app/rootfs"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var gotArgs []string
			gotFlags := map[string]string{}
			sdk := &SDK{Metadata: Metadata{Name: "isle"}}
			tt.configure(sdk, &gotArgs, &gotFlags)

			resp, err := sdk.handleRPC(&cobra.Command{Use: "rpc"}, tt.req)
			if err != nil {
				t.Fatalf("handleRPC() error = %v", err)
			}
			if !resp.OK {
				t.Fatalf("expected OK response, got %+v", resp)
			}
			if !reflect.DeepEqual(gotArgs, tt.wantArgs) {
				t.Fatalf("args = %#v, want %#v", gotArgs, tt.wantArgs)
			}
			if !reflect.DeepEqual(gotFlags, tt.wantFlags) {
				t.Fatalf("flags = %#v, want %#v", gotFlags, tt.wantFlags)
			}
		})
	}
}

func TestExecuteRPCCommandPropagatesContextToSelectedCommand(t *testing.T) {
	t.Parallel()

	root := &cobra.Command{Use: "component"}
	child := &cobra.Command{
		Use: "describe",
		RunE: func(cmd *cobra.Command, args []string) error {
			if got, _ := cmd.Context().Value(rpcContextTestKey).(string); got != "expected" {
				return fmt.Errorf("context value = %q, want expected", got)
			}
			return nil
		},
	}
	root.AddCommand(child)

	runCtx := context.WithValue(context.Background(), rpcContextTestKey, "expected")
	if _, err := executeRPCCommand(runCtx, root, []string{"describe"}); err != nil {
		t.Fatalf("executeRPCCommand() error = %v", err)
	}
}

func TestExecuteRPCCommandRestoresChildContexts(t *testing.T) {
	t.Parallel()

	rootOriginal := context.WithValue(context.Background(), rpcContextTestKey, "root-original")
	childOriginal := context.WithValue(context.Background(), rpcContextTestKey, "child-original")
	runCtx := context.WithValue(context.Background(), rpcContextTestKey, "expected")
	root := &cobra.Command{Use: "component"}
	child := &cobra.Command{
		Use: "describe",
		RunE: func(cmd *cobra.Command, args []string) error {
			if got, _ := cmd.Context().Value(rpcContextTestKey).(string); got != "expected" {
				return fmt.Errorf("context value = %q, want expected", got)
			}
			return nil
		},
	}
	root.SetContext(rootOriginal)
	child.SetContext(childOriginal)
	root.AddCommand(child)

	if _, err := executeRPCCommand(runCtx, root, []string{"describe"}); err != nil {
		t.Fatalf("executeRPCCommand() error = %v", err)
	}
	if got, _ := root.Context().Value(rpcContextTestKey).(string); got != "root-original" {
		t.Fatalf("root context = %q, want root-original", got)
	}
	if got, _ := child.Context().Value(rpcContextTestKey).(string); got != "child-original" {
		t.Fatalf("child context = %q, want child-original", got)
	}
}

func TestExecuteRPCCommandRestoresCommandState(t *testing.T) {
	t.Parallel()

	var originalOut bytes.Buffer
	var originalErr bytes.Buffer
	root := &cobra.Command{
		Use: "root",
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := cmd.OutOrStdout().Write([]byte("captured\n"))
			return err
		},
	}
	root.SetIn(strings.NewReader("original stdin"))
	root.SetOut(&originalOut)
	root.SetErr(&originalErr)
	root.SilenceUsage = false

	output, err := executeRPCCommandWithIO(context.Background(), "test.method", root, nil, rpcCommandIO{
		stdin:  strings.NewReader("rpc stdin"),
		stderr: &bytes.Buffer{},
	})
	if err != nil {
		t.Fatalf("executeRPCCommandWithIO() error = %v", err)
	}
	if output != "captured\n" {
		t.Fatalf("captured output = %q, want captured", output)
	}
	if root.SilenceUsage {
		t.Fatal("expected SilenceUsage to be restored")
	}
	if _, err := root.OutOrStdout().Write([]byte("after\n")); err != nil {
		t.Fatalf("write to restored stdout: %v", err)
	}
	if originalOut.String() != "after\n" {
		t.Fatalf("stdout was not restored, originalOut = %q", originalOut.String())
	}
	if _, err := root.ErrOrStderr().Write([]byte("err after\n")); err != nil {
		t.Fatalf("write to restored stderr: %v", err)
	}
	if originalErr.String() != "err after\n" {
		t.Fatalf("stderr was not restored, originalErr = %q", originalErr.String())
	}
}

func TestExecuteRPCCommandCanMirrorLiveStdout(t *testing.T) {
	t.Parallel()

	var mirrored bytes.Buffer
	root := &cobra.Command{
		Use: "root",
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := cmd.OutOrStdout().Write([]byte("create progress\n"))
			return err
		},
	}

	output, err := executeRPCCommandWithIO(context.Background(), "create.run", root, nil, rpcCommandIO{
		stdin:      strings.NewReader("rpc stdin"),
		stderr:     &mirrored,
		liveStdout: true,
	})
	if err != nil {
		t.Fatalf("executeRPCCommandWithIO() error = %v", err)
	}
	if output != "create progress\n" {
		t.Fatalf("captured output = %q, want create progress", output)
	}
	if mirrored.String() != "create progress\n" {
		t.Fatalf("mirrored output = %q, want create progress", mirrored.String())
	}
}

func TestDiscoveryMetadataInvocationUsesEnvFastPath(t *testing.T) {
	t.Setenv("SITECTL_RPC_METADATA", "1")
	oldArgs := os.Args
	os.Args = []string{"sitectl-child", "__sitectl-rpc"}
	t.Cleanup(func() {
		os.Args = oldArgs
	})

	if !isDiscoveryMetadataInvocation() {
		t.Fatal("expected SITECTL_RPC_METADATA to trigger metadata invocation")
	}
}

func executeRPCCommand(runCtx context.Context, command *cobra.Command, args []string) (string, error) {
	return executeRPCCommandWithIO(runCtx, "test.method", command, args, rpcCommandIO{stdin: os.Stdin, stderr: os.Stderr})
}

func saveValidateTestContext(t *testing.T) {
	t.Helper()

	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)
	if err := config.SaveContext(&config.Context{
		Name:           "museum",
		Site:           "museum",
		Plugin:         "isle",
		DockerHostType: config.ContextLocal,
		DockerSocket:   "/var/run/docker.sock",
		ProjectDir:     tempHome,
	}, true); err != nil {
		t.Fatalf("SaveContext() error = %v", err)
	}
}

func decodeRPCCommandResponse(t *testing.T, data []byte) RPCResponse {
	t.Helper()

	var resp RPCResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("Unmarshal(RPCResponse) error = %v: %s", err, strings.TrimSpace(string(data)))
	}
	return resp
}

func rpcMethodConstants(t *testing.T) []string {
	t.Helper()

	file, err := parser.ParseFile(token.NewFileSet(), "rpc.go", nil, 0)
	if err != nil {
		t.Fatalf("ParseFile(rpc.go) error = %v", err)
	}
	var methods []string
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			valueSpec, ok := spec.(*ast.ValueSpec)
			if !ok || len(valueSpec.Values) == 0 {
				continue
			}
			for i, name := range valueSpec.Names {
				if !strings.HasPrefix(name.Name, "Method") || i >= len(valueSpec.Values) {
					continue
				}
				lit, ok := valueSpec.Values[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				value, err := strconv.Unquote(lit.Value)
				if err != nil {
					t.Fatalf("Unquote(%s) error = %v", lit.Value, err)
				}
				methods = append(methods, value)
			}
		}
	}
	return methods
}

func rpcMethodParamTypes(t *testing.T) map[string]string {
	t.Helper()

	got := map[string]string{}
	for method, spec := range rpcMethodRegistry() {
		if spec.paramsType == nil {
			got[method] = ""
			continue
		}
		got[method] = spec.paramsType.Name()
	}
	return got
}

func assertMarshalJSON(t *testing.T, value any, want string) {
	t.Helper()

	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("Marshal(%T) error = %v", value, err)
	}
	if string(data) != want {
		t.Fatalf("Marshal(%T) = %s, want %s", value, data, want)
	}
}

func assertExactJSONTags(t *testing.T, typ reflect.Type, want map[string]string) {
	t.Helper()

	if typ.Kind() != reflect.Struct {
		t.Fatalf("%s is not a struct", typ)
	}
	seen := map[string]bool{}
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if !field.IsExported() {
			continue
		}
		seen[field.Name] = true
		wantTag, ok := want[field.Name]
		if !ok {
			t.Fatalf("%s.%s is not listed in exact JSON tag contract", typ.Name(), field.Name)
		}
		if tag := string(field.Tag.Get("json")); tag != wantTag {
			t.Fatalf("%s.%s json tag = %q, want %q", typ.Name(), field.Name, tag, wantTag)
		}
	}
	for name := range want {
		if !seen[name] {
			t.Fatalf("%s exact JSON tag contract lists missing field %s", typ.Name(), name)
		}
	}
}

func assertJSONTagsUseLowerSnakeCase(t *testing.T, types []reflect.Type) {
	t.Helper()

	lowerSnake := regexp.MustCompile(`^[a-z][a-z0-9]*(?:_[a-z0-9]+)*$`)
	visited := map[reflect.Type]bool{}
	for _, typ := range types {
		assertJSONTypeUsesLowerSnakeCase(t, typ, typ.Name(), lowerSnake, visited)
	}
}

func assertJSONTypeUsesLowerSnakeCase(t *testing.T, typ reflect.Type, path string, lowerSnake *regexp.Regexp, visited map[reflect.Type]bool) {
	t.Helper()

	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	switch typ.Kind() {
	case reflect.Slice, reflect.Array:
		assertJSONTypeUsesLowerSnakeCase(t, typ.Elem(), path, lowerSnake, visited)
		return
	case reflect.Map:
		assertJSONTypeUsesLowerSnakeCase(t, typ.Elem(), path, lowerSnake, visited)
		return
	case reflect.Struct:
	default:
		return
	}
	if visited[typ] {
		return
	}
	visited[typ] = true

	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if !field.IsExported() {
			continue
		}
		fieldPath := path + "." + field.Name
		tag, ok := field.Tag.Lookup("json")
		if !ok {
			t.Fatalf("%s is missing a json tag", fieldPath)
		}
		name, _, _ := strings.Cut(tag, ",")
		if name == "-" {
			continue
		}
		if !lowerSnake.MatchString(name) {
			t.Fatalf("%s json tag = %q, want lower_snake_case", fieldPath, tag)
		}
		assertJSONTypeUsesLowerSnakeCase(t, field.Type, fieldPath, lowerSnake, visited)
	}
}

func assertRPCParamTypeListComplete(t *testing.T, paramTypes []reflect.Type) {
	t.Helper()

	listed := map[string]bool{}
	for _, typ := range paramTypes {
		listed[typ.Name()] = true
	}
	for _, name := range rpcParamStructNames(t) {
		if !listed[name] {
			t.Fatalf("RPC params contract test is missing %s", name)
		}
	}
}

func rpcParamStructNames(t *testing.T) []string {
	t.Helper()

	file, err := parser.ParseFile(token.NewFileSet(), "rpc.go", nil, 0)
	if err != nil {
		t.Fatalf("ParseFile(rpc.go) error = %v", err)
	}
	var names []string
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.TYPE {
			continue
		}
		for _, spec := range gen.Specs {
			typeSpec, ok := spec.(*ast.TypeSpec)
			if !ok || !strings.HasSuffix(typeSpec.Name.Name, "Params") {
				continue
			}
			if _, ok := typeSpec.Type.(*ast.StructType); ok {
				names = append(names, typeSpec.Name.Name)
			}
		}
	}
	return names
}

func mustRPCRequest(req RPCRequest, err error) RPCRequest {
	if err != nil {
		panic(err)
	}
	return req
}

func captureCommand(use string, gotArgs *[]string, gotFlags *map[string]string) *cobra.Command {
	return &cobra.Command{
		Use:          use,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			*gotArgs = append([]string{}, args...)
			out := map[string]string{}
			cmd.Flags().VisitAll(func(flag *pflag.Flag) {
				if flag.Changed {
					out[flag.Name] = flag.Value.String()
				}
			})
			*gotFlags = out
			return nil
		},
	}
}

type validateRunnerStub struct {
	codebaseRootfs   string
	wantContextValue string
	stdout           string
}

func (r *validateRunnerStub) BindFlags(cmd *cobra.Command) {
	cmd.Flags().StringVar(&r.codebaseRootfs, "codebase-rootfs", "", "")
}

func (r *validateRunnerStub) Run(cmd *cobra.Command, ctx *config.Context) ([]sitevalidate.Result, error) {
	if r.wantContextValue != "" {
		got, _ := cmd.Context().Value(rpcContextTestKey).(string)
		if got != r.wantContextValue {
			return nil, fmt.Errorf("context value = %q, want %q", got, r.wantContextValue)
		}
	}
	if r.stdout != "" {
		fmt.Fprintln(cmd.OutOrStdout(), r.stdout)
	}
	return []sitevalidate.Result{{
		Name:   "codebase-rootfs",
		Status: sitevalidate.StatusOK,
		Detail: r.codebaseRootfs,
	}}, nil
}

type validateLifecycleRunner struct {
	preRunCalled *bool
}

func (r *validateLifecycleRunner) BindFlags(cmd *cobra.Command) {
	cmd.Flags().String("codebase-rootfs", "", "")
	cmd.PreRunE = func(cmd *cobra.Command, args []string) error {
		*r.preRunCalled = true
		return nil
	}
}

func (r *validateLifecycleRunner) Run(cmd *cobra.Command, ctx *config.Context) ([]sitevalidate.Result, error) {
	if !*r.preRunCalled {
		return nil, fmt.Errorf("validate PreRunE did not run")
	}
	return []sitevalidate.Result{{
		Name:   "lifecycle",
		Status: sitevalidate.StatusOK,
	}}, nil
}

type healthcheckRunnerStub struct{}

func (r *healthcheckRunnerStub) BindFlags(cmd *cobra.Command) {
}

func (r *healthcheckRunnerStub) Run(cmd *cobra.Command, ctx *config.Context) ([]sitevalidate.Result, error) {
	return []sitevalidate.Result{{
		Name:   "healthcheck-rootfs",
		Status: sitevalidate.StatusOK,
	}}, nil
}

type debugRunnerStub struct{}

func (debugRunnerStub) BindFlags(cmd *cobra.Command) {
	cmd.Flags().Bool("verbose", false, "")
}

func (debugRunnerStub) Render(cmd *cobra.Command, ctx *config.Context) (string, error) {
	return "debug", nil
}

type debugRunnerWithoutFlags struct{}

func (debugRunnerWithoutFlags) BindFlags(cmd *cobra.Command) {}

func (debugRunnerWithoutFlags) Render(cmd *cobra.Command, ctx *config.Context) (string, error) {
	return "debug", nil
}

type setBridgeValidationRunner struct{}

func (setBridgeValidationRunner) BindFlags(cmd *cobra.Command) {
	cmd.Flags().String("path", "", "")
}

func (setBridgeValidationRunner) Run(cmd *cobra.Command, args []string, ctx *config.Context) error {
	return nil
}

type convergeBridgeValidationRunner struct{}

func (convergeBridgeValidationRunner) BindFlags(cmd *cobra.Command) {
	cmd.Flags().String("path", "", "")
	cmd.Flags().String("codebase-rootfs", "", "")
	cmd.Flags().Bool("report", false, "")
	cmd.Flags().Bool("verbose", false, "")
	cmd.Flags().String("format", "", "")
}

func (convergeBridgeValidationRunner) Run(cmd *cobra.Command, ctx *config.Context) error {
	return nil
}

type deployRunnerStub struct{}

func (deployRunnerStub) BindFlags(cmd *cobra.Command) {}

func (deployRunnerStub) PreDown(cmd *cobra.Command, ctx *config.Context) error {
	return nil
}

func (deployRunnerStub) PostUp(cmd *cobra.Command, ctx *config.Context) error {
	return nil
}

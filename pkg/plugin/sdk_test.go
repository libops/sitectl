package plugin

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/libops/sitectl/pkg/config"
	"github.com/libops/sitectl/pkg/validate"
)

func TestGetContextAllowsIncludedPlugin(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	ctx := config.Context{
		Name:           "museum",
		Site:           "museum",
		Plugin:         "isle",
		DockerHostType: config.ContextLocal,
		Environment:    "local",
		DockerSocket:   "/var/run/docker.sock",
		ProjectName:    "museum",
		ProjectDir:     tempHome,
	}
	if err := config.SaveContext(&ctx, true); err != nil {
		t.Fatalf("SaveContext() error = %v", err)
	}

	sdk := NewSDK(Metadata{Name: "drupal"})
	sdk.Config.Context = "museum"

	got, err := sdk.GetContext()
	if err != nil {
		t.Fatalf("GetContext() error = %v", err)
	}
	if got.Plugin != "isle" {
		t.Fatalf("expected context plugin isle, got %q", got.Plugin)
	}
}

func TestGetContextSupportsDotContext(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	projectDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectDir, "docker-compose.yml"), []byte("services:\n  drupal:\n    image: drupal:latest\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(docker-compose.yml) error = %v", err)
	}

	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(projectDir); err != nil {
		t.Fatalf("Chdir(projectDir) error = %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(oldWD)
	})

	previousDetector := config.SetProjectClaimDetector(nil)
	sdk := NewSDK(Metadata{Name: "isle"})
	t.Cleanup(func() {
		config.SetProjectClaimDetector(previousDetector)
	})
	sdk.SetProjectDiscovery(func(projectDir string) (*config.ProjectClaim, error) {
		return &config.ProjectClaim{Plugin: "isle", ProjectDir: projectDir, Reason: "test claim"}, nil
	})
	sdk.Config.Context = "."

	got, err := sdk.GetContext()
	if err != nil {
		t.Fatalf("GetContext() error = %v", err)
	}
	expectedProjectDir, err := filepath.Abs(projectDir)
	if err != nil {
		t.Fatalf("Abs(projectDir) error = %v", err)
	}
	if resolved, err := filepath.EvalSymlinks(expectedProjectDir); err == nil {
		expectedProjectDir = resolved
	}
	if got.Name != "." || got.Plugin != "isle" || got.ProjectDir != expectedProjectDir || !got.Ephemeral {
		t.Fatalf("unexpected transient context: %+v", got)
	}
}

func TestGetContextRejectsUnsupportedPlugin(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	ctx := config.Context{
		Name:           "museum",
		Site:           "museum",
		Plugin:         "drupal",
		DockerHostType: config.ContextLocal,
		Environment:    "local",
		DockerSocket:   "/var/run/docker.sock",
		ProjectName:    "museum",
		ProjectDir:     tempHome,
	}
	if err := config.SaveContext(&ctx, true); err != nil {
		t.Fatalf("SaveContext() error = %v", err)
	}

	sdk := NewSDK(Metadata{Name: "isle"})
	sdk.Config.Context = "museum"

	if _, err := sdk.GetContext(); err == nil {
		t.Fatal("expected plugin compatibility error")
	}
}

func TestContextPluginSupportsBuiltinHierarchy(t *testing.T) {
	if !ContextPluginSupports("isle", "drupal") {
		t.Fatal("expected isle contexts to support drupal")
	}
	if ContextPluginSupports("drupal", "isle") {
		t.Fatal("did not expect drupal contexts to support isle")
	}
}

func TestPluginIncludesMergesBuiltinAndInstalledWithoutDuplicates(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	writeRPCFixturePlugin(t, dir, "sitectl-isle", InstalledPlugin{
		ProtocolVersion: RPCProtocolVersion,
		Name:            "isle",
		Includes:        []string{"drupal", "libops"},
	}, "create-help")

	got := pluginIncludes("isle")
	want := []string{"drupal", "libops"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("pluginIncludes() = %v, want %v", got, want)
	}
}

func TestInvokePluginRPCPassesContextAndLogLevelOverStdin(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("COLUMNS", "123")

	writePluginFixture(t, dir, "sitectl-child", "rpc-echo-request.sh")

	sdk := NewSDK(Metadata{Name: "isle"})
	sdk.Config.Context = "demo"
	sdk.Config.LogLevel = "DEBUG"

	resp, err := sdk.InvokePluginRPC("child", NewRPCRequest(MethodDebugRun), CommandExecOptions{})
	if err != nil {
		t.Fatalf("InvokePluginRPC() error = %v", err)
	}
	if !strings.Contains(resp.Output, "ARGS=__sitectl-rpc") {
		t.Fatalf("expected rpc args in output, got %q", resp.Output)
	}
	if !strings.Contains(resp.Output, "COLUMNS=123") {
		t.Fatalf("expected COLUMNS env in output, got %q", resp.Output)
	}
	requestLine := ""
	for _, line := range strings.Split(resp.Output, "\n") {
		if strings.HasPrefix(line, "REQUEST_B64=") {
			requestLine = strings.TrimPrefix(line, "REQUEST_B64=")
		}
	}
	requestData, err := base64.StdEncoding.DecodeString(requestLine)
	if err != nil {
		t.Fatalf("DecodeString() error = %v", err)
	}
	req, err := decodeRPCRequest(requestData)
	if err != nil {
		t.Fatalf("decodeRPCRequest() error = %v", err)
	}
	if req.Context != "demo" || req.LogLevel != "DEBUG" || req.Method != MethodDebugRun {
		t.Fatalf("unexpected request: %+v", req)
	}
}

func TestInvokePluginRPCUsesRequestFlagWhenStdinIsReserved(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sitectl-interactive")

	writePluginFixture(t, dir, "sitectl-interactive", "rpc-echo-stdin.sh")

	sdk := NewSDK(Metadata{Name: "isle"})
	resp, err := sdk.invokePluginRPCPath("interactive", path, NewRPCRequest(MethodDebugRun), CommandExecOptions{
		Stdin: strings.NewReader("interactive input"),
	})
	if err != nil {
		t.Fatalf("InvokePluginRPC() error = %v", err)
	}
	if !strings.Contains(resp.Output, "ARGS=__sitectl-rpc --request ") {
		t.Fatalf("expected --request argv in output, got %q", resp.Output)
	}
	if !strings.Contains(resp.Output, "STDIN=interactive input") {
		t.Fatalf("expected interactive stdin in output, got %q", resp.Output)
	}
}

func TestInvokePluginRPCRejectsSensitiveParamsOnRequestFlagPath(t *testing.T) {
	req, err := withRPCParams(NewRPCRequest(MethodDebugRun), sensitiveRPCParamsForTest{Token: "secret"})
	if err != nil {
		t.Fatalf("withRPCParams() error = %v", err)
	}

	sdk := NewSDK(Metadata{Name: "isle"})
	_, err = sdk.invokePluginRPCPath("interactive", filepath.Join(t.TempDir(), "sitectl-interactive"), req, CommandExecOptions{
		Stdin: strings.NewReader("interactive input"),
	})
	if err == nil {
		t.Fatal("expected sensitive params argv transport error")
	}
	if !strings.Contains(err.Error(), "sensitive params") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestInvokePluginRPCRejectsUnvalidatedManualParamsOnRequestFlagPath(t *testing.T) {
	req := NewRPCRequest(MethodDebugRun)
	params, err := RPCParams(sensitiveRPCParamsForTest{Token: "secret"})
	if err != nil {
		t.Fatalf("RPCParams() error = %v", err)
	}
	req.Params = params

	sdk := NewSDK(Metadata{Name: "isle"})
	_, err = sdk.invokePluginRPCPath("interactive", filepath.Join(t.TempDir(), "sitectl-interactive"), req, CommandExecOptions{
		Stdin: strings.NewReader("interactive input"),
	})
	if err == nil {
		t.Fatal("expected manual params argv transport error")
	}
	if !strings.Contains(err.Error(), "cannot use argv transport") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRPCArgvSafetyDoesNotScreenPassthroughArgs(t *testing.T) {
	req, err := NewComponentSetRequest(ComponentSetParams{Name: "captcha", Disposition: "enabled"}, "--turnstile-secret", "secret")
	if err != nil {
		t.Fatalf("NewComponentSetRequest() error = %v", err)
	}

	// Component follow-up flags ride in Args. This locks the current boundary:
	// the argv safety gate screens typed Params only, so follow-ups must not
	// collect secrets.
	if err := ensureRPCRequestArgvSafe(req); err != nil {
		t.Fatalf("ensureRPCRequestArgvSafe() error = %v, want Args boundary to remain caller-trusted", err)
	}
}

func TestInvokePluginRPCReturnsStderrDetail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sitectl-broken")

	script := `#!/bin/sh
	echo "something went wrong" >&2
	exit 2
	`
	writePluginScript(t, dir, "sitectl-broken", script)

	sdk := NewSDK(Metadata{Name: "isle"})
	_, err := sdk.invokePluginRPCPath("broken", path, NewRPCRequest(MethodDebugRun), CommandExecOptions{})
	if err == nil {
		t.Fatal("expected InvokePluginRPC() error")
	}
	var processErr *RPCProcessError
	if !errors.As(err, &processErr) {
		t.Fatalf("expected RPCProcessError, got %T", err)
	}
	if processErr.ExitCode != 2 || processErr.Detail != "something went wrong" {
		t.Fatalf("unexpected process error: %+v", processErr)
	}
	if !strings.Contains(err.Error(), "something went wrong") {
		t.Fatalf("expected stderr detail in error, got %v", err)
	}
}

func TestInvokePluginRPCCanMirrorLiveStderr(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sitectl-noisy")

	writeRPCOutputFixturePlugin(t, dir, "sitectl-noisy", "stdout payload", "visible stderr")

	sdk := NewSDK(Metadata{Name: "isle"})
	var stderr bytes.Buffer
	resp, err := sdk.invokePluginRPCPath("noisy", path, NewRPCRequest(MethodDebugRun), CommandExecOptions{
		LiveStderr: true,
		Stderr:     &stderr,
	})
	if err != nil {
		t.Fatalf("InvokePluginRPC() error = %v", err)
	}
	if !strings.Contains(stderr.String(), "visible stderr") {
		t.Fatalf("expected mirrored stderr, got %q", stderr.String())
	}
	if !strings.Contains(resp.Output, "stdout payload") {
		t.Fatalf("expected stdout payload, got %q", resp.Output)
	}
}

func TestInvokePluginRPCValidatesResponseEnvelope(t *testing.T) {
	tests := []struct {
		name            string
		response        string
		wantError       string
		wantOK          bool
		wantFailureCode string
	}{
		{
			name:      "protocol mismatch",
			response:  rpcResponseEnvelopeJSON(t, RPCResponse{ProtocolVersion: 2, OK: true}),
			wantError: "rebuild or reinstall the plugin to match sitectl",
		},
		{
			name: "rpc error message",
			response: rpcResponseEnvelopeJSON(t, RPCResponse{
				ProtocolVersion: RPCProtocolVersion,
				OK:              false,
				Error: &RPCError{
					Code:    RPCErrorCodeNotRegistered,
					Message: "clean failure",
				},
			}),
			wantError:       "clean failure",
			wantOK:          false,
			wantFailureCode: RPCErrorCodeNotRegistered,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "sitectl-envelope")
			writeRPCResponseFixturePlugin(t, dir, "sitectl-envelope", tt.response, rpcFixtureEnv{})

			sdk := NewSDK(Metadata{Name: "isle"})
			resp, err := sdk.invokePluginRPCPath("envelope", path, NewRPCRequest(MethodDebugRun), CommandExecOptions{})
			if err == nil {
				t.Fatal("expected InvokePluginRPC() error")
			}
			if !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("expected error containing %q, got %v", tt.wantError, err)
			}
			if tt.wantFailureCode != "" {
				var failure *RPCFailure
				if !errors.As(err, &failure) {
					t.Fatalf("expected RPCFailure, got %T", err)
				}
				if failure.Code != tt.wantFailureCode {
					t.Fatalf("expected RPCFailure code %q, got %+v", tt.wantFailureCode, failure)
				}
			}
			if resp.OK != tt.wantOK {
				t.Fatalf("expected response OK=%v, got %+v", tt.wantOK, resp)
			}
		})
	}
}

func TestInvokePluginRPCAcceptsMissingResponseProtocolVersion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sitectl-legacy")
	writeRPCResponseFixturePlugin(t, dir, "sitectl-legacy", `{"ok":true,"output":"legacy response"}`, rpcFixtureEnv{})

	sdk := NewSDK(Metadata{Name: "isle"})
	resp, err := sdk.invokePluginRPCPath("legacy", path, NewRPCRequest(MethodDebugRun), CommandExecOptions{})
	if err != nil {
		t.Fatalf("InvokePluginRPC() error = %v", err)
	}
	if resp.ProtocolVersion != RPCProtocolVersion {
		t.Fatalf("expected defaulted protocol version %d, got %+v", RPCProtocolVersion, resp)
	}
	if resp.Output != "legacy response" {
		t.Fatalf("expected legacy response output, got %q", resp.Output)
	}
}

func TestInvokePluginRPCParsesLastStdoutLineEnvelope(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sitectl-noisy-envelope")
	writeRPCResponseFixturePlugin(t, dir, "sitectl-noisy-envelope", "stray stdout\n"+rpcOutputResponseJSON(t, "clean response"), rpcFixtureEnv{})

	sdk := NewSDK(Metadata{Name: "isle"})
	resp, err := sdk.invokePluginRPCPath("noisy-envelope", path, NewRPCRequest(MethodDebugRun), CommandExecOptions{})
	if err != nil {
		t.Fatalf("InvokePluginRPC() error = %v", err)
	}
	if resp.Output != "clean response" {
		t.Fatalf("expected clean response output, got %q", resp.Output)
	}
}

func TestParsePluginRPCResponseWarnsOnStdoutFallback(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() {
		slog.SetDefault(previous)
	})

	resp, err := parsePluginRPCResponse("noisy", MethodDebugRun, []byte("stray stdout\n"+rpcOutputResponseJSON(t, "clean response")+"\n"))
	if err != nil {
		t.Fatalf("parsePluginRPCResponse() error = %v", err)
	}
	if resp.Output != "clean response" {
		t.Fatalf("expected clean response output, got %q", resp.Output)
	}
	if !strings.Contains(logs.String(), "plugin wrote stdout outside rpc envelope") || !strings.Contains(logs.String(), "cmd.OutOrStdout()") || !strings.Contains(logs.String(), "fallback_offset=13") {
		t.Fatalf("expected stdout fallback warning, got %q", logs.String())
	}
}

func TestParsePluginRPCResponseRejectsLastJSONLineWithoutEnvelope(t *testing.T) {
	_, err := parsePluginRPCResponse("noisy", MethodDebugRun, []byte(rpcOutputResponseJSON(t, "real response")+"\n"+`{"message":"diagnostic"}`+"\n"))
	if err == nil {
		t.Fatal("expected non-envelope JSON line to be rejected")
	}
	if !strings.Contains(err.Error(), "non-envelope JSON line") || !strings.Contains(err.Error(), "cmd.OutOrStdout()") {
		t.Fatalf("expected non-envelope stdout hint, got %v", err)
	}
}

func TestInvokePluginRPCReportsActionableStdoutParseHint(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sitectl-polluted")
	writePluginScript(t, dir, "sitectl-polluted", `#!/bin/sh
printf '%s\n' 'plugin diagnostic on stdout'
printf '%s\n' 'not json'
`)

	sdk := NewSDK(Metadata{Name: "isle"})
	_, err := sdk.invokePluginRPCPath("polluted", path, NewRPCRequest(MethodDebugRun), CommandExecOptions{})
	if err == nil {
		t.Fatal("expected stdout parse error")
	}
	if !strings.Contains(err.Error(), "plugin wrote non-JSON to stdout") || !strings.Contains(err.Error(), "cmd.OutOrStdout()") {
		t.Fatalf("expected actionable stdout hint, got %v", err)
	}
}

func TestInvokePluginRPCSetsMetadataFastPathEnv(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "sitectl-envcheck")
	writePluginFixture(t, dir, "sitectl-envcheck", "rpc-metadata-env.sh")

	sdk := NewSDK(Metadata{Name: "isle"})
	metadataResp, err := sdk.invokePluginRPCPath("envcheck", path, NewRPCRequest(MethodPluginMetadata), CommandExecOptions{})
	if err != nil {
		t.Fatalf("metadata InvokePluginRPC() error = %v", err)
	}
	if !strings.Contains(metadataResp.Output, "metadata=1") {
		t.Fatalf("expected metadata env marker, got %q", metadataResp.Output)
	}

	debugResp, err := sdk.invokePluginRPCPath("envcheck", path, NewRPCRequest(MethodDebugRun), CommandExecOptions{})
	if err != nil {
		t.Fatalf("debug InvokePluginRPC() error = %v", err)
	}
	if strings.Contains(debugResp.Output, "metadata=1") {
		t.Fatalf("did not expect metadata env marker for debug, got %q", debugResp.Output)
	}
}

func TestRPCCommandRetainsStdoutOnError(t *testing.T) {
	sdk := NewSDK(Metadata{Name: "isle"})
	cmd := &cobra.Command{
		Use: "failing",
		RunE: func(cmd *cobra.Command, args []string) error {
			_, _ = cmd.OutOrStdout().Write([]byte("partial output\n"))
			return os.ErrInvalid
		},
	}

	rpcCmd := &cobra.Command{Use: "rpc"}
	rpcCmd.SetContext(context.Background())
	resp, err := sdk.rpcCommand(rpcCmd, MethodDebugRun, cmd, nil)
	if err != nil {
		t.Fatalf("rpcCommand() error = %v", err)
	}
	if resp.OK {
		t.Fatal("expected error response")
	}
	if resp.Output != "partial output\n" {
		t.Fatalf("expected captured output, got %q", resp.Output)
	}
	if resp.Error == nil || !strings.Contains(resp.Error.Message, os.ErrInvalid.Error()) {
		t.Fatalf("expected structured command error, got %+v", resp.Error)
	}
}

func TestInvokePluginRPCRejectsOversizedResponse(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sitectl-huge")
	writePluginScript(t, dir, "sitectl-huge", `#!/bin/sh
dd if=/dev/zero bs=1048576 count=5 2>/dev/null | tr '\000' x
`)

	sdk := NewSDK(Metadata{Name: "isle"})
	_, err := sdk.invokePluginRPCPath("huge", path, NewRPCRequest(MethodDebugRun), CommandExecOptions{})
	if err == nil {
		t.Fatal("expected oversized response error")
	}
	if !strings.Contains(err.Error(), "rpc response") || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(err.Error(), MethodDebugRun) || !strings.Contains(err.Error(), "use --format json") || !strings.Contains(err.Error(), "narrow") {
		t.Fatalf("expected actionable oversized response guidance, got %v", err)
	}
}

func TestInvokeIncludedPluginRPCRejectsUnincludedPlugin(t *testing.T) {
	sdk := NewSDK(Metadata{Name: "isle", Includes: []string{"drupal"}})

	_, err := sdk.InvokeIncludedPluginRPC("libops", NewRPCRequest(MethodDebugRun), CommandExecOptions{})
	if err == nil {
		t.Fatal("expected included plugin validation error")
	}
	if !strings.Contains(err.Error(), `is not included by "isle"`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestContextValidatorsReturnsCopy(t *testing.T) {
	sdk := NewSDK(Metadata{Name: "isle"})
	first := validate.Validator(func(*config.Context) ([]validate.Result, error) { return nil, nil })
	second := validate.Validator(func(*config.Context) ([]validate.Result, error) { return nil, nil })

	sdk.RegisterContextValidator(first)
	sdk.RegisterContextValidator(nil)
	sdk.RegisterContextValidator(second)

	got := sdk.ContextValidators()
	if len(got) != 2 {
		t.Fatalf("expected 2 validators, got %d", len(got))
	}

	got[0] = nil
	again := sdk.ContextValidators()
	if again[0] == nil {
		t.Fatal("expected ContextValidators() to return a copy")
	}
}

func TestRegisterCreateRunnerExposesDefinitions(t *testing.T) {
	sdk := NewSDK(Metadata{Name: "isle"})
	sdk.RegisterCreateRunner(CreateSpec{
		Name:              "default",
		Description:       "Create an ISLE stack",
		Default:           true,
		DockerComposeRepo: "https://github.com/example/isle",
	}, createRunnerStub{})

	defs := sdk.CreateDefinitions()
	if len(defs) != 1 {
		t.Fatalf("expected 1 create definition, got %d", len(defs))
	}
	if defs[0].Name != "default" {
		t.Fatalf("expected default create definition, got %+v", defs[0])
	}
	if sdk.createRootCmd == nil {
		t.Fatal("expected hidden create root command to be registered")
	}
}

func TestRegisterCreateRunnerHonorsMetadataFastPath(t *testing.T) {
	spec := CreateSpec{Name: "default", Description: "Create"}

	t.Setenv("SITECTL_RPC_METADATA", "")
	normalRunner := &createBindCounter{}
	normalSDK := NewSDK(Metadata{Name: "isle"})
	normalSDK.RegisterCreateRunner(spec, normalRunner)
	if !normalRunner.bound {
		t.Fatal("expected normal create registration to bind flags")
	}

	t.Setenv("SITECTL_RPC_METADATA", "1")
	metadataRunner := &createBindCounter{}
	metadataSDK := NewSDK(Metadata{Name: "isle"})
	metadataSDK.RegisterCreateRunner(spec, metadataRunner)
	if metadataRunner.bound {
		t.Fatal("expected metadata fast-path create registration to skip BindFlags")
	}
}

type createRunnerStub struct{}

func (createRunnerStub) BindFlags(cmd *cobra.Command) {}

func (createRunnerStub) Run(cmd *cobra.Command) error { return nil }

type createBindCounter struct {
	bound bool
}

func (r *createBindCounter) BindFlags(cmd *cobra.Command) {
	r.bound = true
}

func (r *createBindCounter) Run(cmd *cobra.Command) error { return nil }

type sensitiveRPCParamsForTest struct {
	Token string `json:"token" rpc_sensitive:"true"`
}

func TestRegisterStandardComposeTemplateAddsLifecycleCommands(t *testing.T) {
	sdk := NewSDK(Metadata{Name: "demo"})
	sdk.RegisterStandardComposeTemplate(CreateSpec{
		Name:                 "default",
		DockerComposeRepo:    "https://github.com/example/demo",
		DockerComposeBuild:   []string{"make build"},
		DockerComposeInit:    []string{"make init"},
		DockerComposeUp:      []string{"make up"},
		DockerComposeDown:    []string{"make down"},
		DockerComposeRollout: []string{"make rollout"},
	}, StandardComposeTemplateOptions{
		DefaultPath:   "./demo",
		DefaultPlugin: "demo",
		DisplayName:   "Demo",
	})

	defs := sdk.CreateDefinitions()
	if len(defs) != 1 {
		t.Fatalf("expected 1 create definition, got %d", len(defs))
	}
	if defs[0].DockerComposeRollout[0] != "make rollout" {
		t.Fatalf("expected rollout command in create definition, got %+v", defs[0].DockerComposeRollout)
	}
	for _, name := range []string{"build", "init", "up", "down", "status", "logs", "rollout"} {
		if _, _, err := sdk.RootCmd.Find([]string{name}); err != nil {
			t.Fatalf("expected %q command to be registered: %v", name, err)
		}
	}
}

func TestDockerComposeExecCommandQuotesArgs(t *testing.T) {
	got := DockerComposeExecCommand("wp", "wp", "--path=/var/www/wp", "post", "get", "hello's world")
	want := "'docker' 'compose' 'exec' '-T' 'wp' 'wp' '--path=/var/www/wp' 'post' 'get' 'hello'\\''s world'"
	if got != want {
		t.Fatalf("DockerComposeExecCommand() = %q, want %q", got, want)
	}
}

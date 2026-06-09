package cmd

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/libops/sitectl/pkg/config"
	"github.com/libops/sitectl/pkg/plugin"
	sitevalidate "github.com/libops/sitectl/pkg/validate"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

func TestExtractConvergeRPCParamsPromotesHostFlags(t *testing.T) {
	params, passthrough, err := extractConvergeRPCParams([]string{
		"--path", "/srv/site",
		"--drupal-rootfs=app/rootfs",
		"--report",
		"--verbose=false",
		"--format", "json",
		"--component", "fcrepo",
	})
	if err != nil {
		t.Fatalf("extractConvergeRPCParams() error = %v", err)
	}

	wantParams := plugin.ConvergeRunParams{
		Path:           "/srv/site",
		CodebaseRootfs: "app/rootfs",
		Report:         true,
		Verbose:        false,
		Format:         "json",
	}
	if !reflect.DeepEqual(params, wantParams) {
		t.Fatalf("params = %+v, want %+v", params, wantParams)
	}
	wantPassthrough := []string{"--component", "fcrepo"}
	if !reflect.DeepEqual(passthrough, wantPassthrough) {
		t.Fatalf("passthrough = %#v, want %#v", passthrough, wantPassthrough)
	}
}

func TestExtractSetRPCParamsPromotesPathOnly(t *testing.T) {
	params, passthrough, err := extractSetRPCParams([]string{
		"--path=/srv/site",
		"--plugin-flag", "value",
	})
	if err != nil {
		t.Fatalf("extractSetRPCParams() error = %v", err)
	}

	if params.Path != "/srv/site" {
		t.Fatalf("Path = %q, want /srv/site", params.Path)
	}
	wantPassthrough := []string{"--plugin-flag", "value"}
	if !reflect.DeepEqual(passthrough, wantPassthrough) {
		t.Fatalf("passthrough = %#v, want %#v", passthrough, wantPassthrough)
	}
}

func TestExtractValidateRPCParamsPromotesReportFormatAndRootfs(t *testing.T) {
	format, params, passthrough, err := extractValidateRPCParams([]string{
		"--format", "json",
		"--codebase-rootfs", "app/rootfs",
		"--strict",
	})
	if err != nil {
		t.Fatalf("extractValidateRPCParams() error = %v", err)
	}

	if format != "json" {
		t.Fatalf("format = %q, want json", format)
	}
	if params.CodebaseRootfs != "app/rootfs" {
		t.Fatalf("CodebaseRootfs = %q, want app/rootfs", params.CodebaseRootfs)
	}
	wantPassthrough := []string{"--strict"}
	if !reflect.DeepEqual(passthrough, wantPassthrough) {
		t.Fatalf("passthrough = %#v, want %#v", passthrough, wantPassthrough)
	}
}

func TestExtractComponentSetRPCParamsSeparatesTargetAndPassthrough(t *testing.T) {
	params, passthrough, err := extractComponentSetRPCParams([]string{
		"--path", "/srv/site",
		"--state=on",
		"--disposition", "enabled",
		"--yolo=false",
		"fcrepo",
		"distributed",
		"--tls-mode", "letsencrypt",
		"--custom", "x",
	})
	if err != nil {
		t.Fatalf("extractComponentSetRPCParams() error = %v", err)
	}

	wantParams := plugin.ComponentSetParams{
		Name:            "fcrepo",
		Disposition:     "distributed",
		Path:            "/srv/site",
		State:           "on",
		DispositionFlag: "enabled",
		Yolo:            false,
	}
	if !reflect.DeepEqual(params, wantParams) {
		t.Fatalf("params = %+v, want %+v", params, wantParams)
	}
	wantPassthrough := []string{"--tls-mode", "letsencrypt", "--custom", "x"}
	if !reflect.DeepEqual(passthrough, wantPassthrough) {
		t.Fatalf("passthrough = %#v, want %#v", passthrough, wantPassthrough)
	}
}

func TestExtractComponentSetRPCParamsRejectsUnknownFlagBeforeComponent(t *testing.T) {
	_, _, err := extractComponentSetRPCParams([]string{
		"--force",
		"fcrepo",
		"off",
	})
	if err == nil {
		t.Fatal("expected component name error")
	}
	if !strings.Contains(err.Error(), "component name is required") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestExtractKnownRPCFlagsStopsAtSeparator(t *testing.T) {
	params, passthrough, err := extractConvergeRPCParams([]string{
		"--path", "/srv/site",
		"--",
		"--report",
		"--format", "json",
	})
	if err != nil {
		t.Fatalf("extractConvergeRPCParams() error = %v", err)
	}
	if params.Path != "/srv/site" {
		t.Fatalf("Path = %q, want /srv/site", params.Path)
	}
	if params.Report {
		t.Fatal("expected --report after separator to remain passthrough")
	}
	wantPassthrough := []string{"--", "--report", "--format", "json"}
	if !reflect.DeepEqual(passthrough, wantPassthrough) {
		t.Fatalf("passthrough = %#v, want %#v", passthrough, wantPassthrough)
	}
}

func TestExtractRPCParamsReportsMissingValue(t *testing.T) {
	_, _, _, err := extractValidateRPCParams([]string{"--format"})
	if err == nil {
		t.Fatal("expected missing value error")
	}
	if !strings.Contains(err.Error(), "--format requires a value") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRPCParamsRoundTripFromHostArgvToPluginCommand(t *testing.T) {
	tests := []struct {
		name       string
		argv       []string
		configure  func(t *testing.T, sdk *plugin.SDK, argv []string) (plugin.RPCRequest, *rpcRoundTripCapture)
		wantArgs   []string
		wantFlags  map[string]string
		wantFormat string
	}{
		{
			name: "converge.run",
			argv: []string{
				"--path", "/srv/site",
				"--drupal-rootfs=app/rootfs",
				"--report",
				"--verbose",
				"--format", "json",
				"--component", "fcrepo",
			},
			configure: func(t *testing.T, sdk *plugin.SDK, argv []string) (plugin.RPCRequest, *rpcRoundTripCapture) {
				t.Helper()
				params, passthrough, err := extractConvergeRPCParams(argv)
				if err != nil {
					t.Fatalf("extractConvergeRPCParams() error = %v", err)
				}
				capture := &rpcRoundTripCapture{}
				sdk.RegisterConvergeRunner(convergeRoundTripRunner{
					capture: capture,
					bind: func(cmd *cobra.Command) {
						cmd.Flags().String("path", "", "")
						cmd.Flags().String("drupal-rootfs", "", "")
						plugin.MarkCodebaseRootfsFlag(cmd, "drupal-rootfs")
						cmd.Flags().Bool("report", false, "")
						cmd.Flags().Bool("verbose", false, "")
						cmd.Flags().String("format", "", "")
						cmd.Flags().String("component", "", "")
					},
				})
				req, err := plugin.NewConvergeRunRequest(params, passthrough...)
				if err != nil {
					t.Fatalf("NewConvergeRunRequest() error = %v", err)
				}
				return req, capture
			},
			wantArgs: []string{},
			wantFlags: map[string]string{
				"path":          "/srv/site",
				"drupal-rootfs": "app/rootfs",
				"report":        "true",
				"verbose":       "true",
				"format":        "json",
				"component":     "fcrepo",
			},
		},
		{
			name: "set.run",
			argv: []string{
				"--path=/srv/site",
				"fcrepo",
				"off",
				"--plugin-flag", "x",
			},
			configure: func(t *testing.T, sdk *plugin.SDK, argv []string) (plugin.RPCRequest, *rpcRoundTripCapture) {
				t.Helper()
				params, passthrough, err := extractSetRPCParams(argv)
				if err != nil {
					t.Fatalf("extractSetRPCParams() error = %v", err)
				}
				capture := &rpcRoundTripCapture{}
				sdk.RegisterSetRunner(setRoundTripRunner{
					capture: capture,
					bind: func(cmd *cobra.Command) {
						cmd.Flags().String("path", "", "")
						cmd.Flags().String("plugin-flag", "", "")
					},
				})
				req, err := plugin.NewSetRunRequest(params, passthrough...)
				if err != nil {
					t.Fatalf("NewSetRunRequest() error = %v", err)
				}
				return req, capture
			},
			wantArgs:  []string{"fcrepo", "off"},
			wantFlags: map[string]string{"path": "/srv/site", "plugin-flag": "x"},
		},
		{
			name: "validate.run",
			argv: []string{
				"--format", "json",
				"--drupal-rootfs", "app/rootfs",
				"--strict",
			},
			configure: func(t *testing.T, sdk *plugin.SDK, argv []string) (plugin.RPCRequest, *rpcRoundTripCapture) {
				t.Helper()
				format, params, passthrough, err := extractValidateRPCParams(argv)
				if err != nil {
					t.Fatalf("extractValidateRPCParams() error = %v", err)
				}
				capture := &rpcRoundTripCapture{format: format}
				sdk.RegisterValidateRunner(validateRoundTripRunner{
					capture: capture,
					bind: func(cmd *cobra.Command) {
						cmd.Flags().String("drupal-rootfs", "", "")
						plugin.MarkCodebaseRootfsFlag(cmd, "drupal-rootfs")
						cmd.Flags().Bool("strict", false, "")
					},
				})
				req, err := plugin.NewValidateRunRequest(params, passthrough...)
				if err != nil {
					t.Fatalf("NewValidateRunRequest() error = %v", err)
				}
				return req, capture
			},
			wantArgs:   []string{},
			wantFlags:  map[string]string{"drupal-rootfs": "app/rootfs", "strict": "true"},
			wantFormat: "json",
		},
		{
			name: "component.set",
			argv: []string{
				"--path", "/srv/site",
				"--state=on",
				"--disposition", "enabled",
				"--yolo",
				"fcrepo",
				"distributed",
				"--tls-mode", "letsencrypt",
				"--custom", "x",
			},
			configure: func(t *testing.T, sdk *plugin.SDK, argv []string) (plugin.RPCRequest, *rpcRoundTripCapture) {
				t.Helper()
				params, passthrough, err := extractComponentSetRPCParams(argv)
				if err != nil {
					t.Fatalf("extractComponentSetRPCParams() error = %v", err)
				}
				capture := &rpcRoundTripCapture{}
				root := &cobra.Command{Use: "component"}
				set := &cobra.Command{
					Use:  "set <name> [disposition]",
					Args: cobra.RangeArgs(1, 2),
					RunE: func(cmd *cobra.Command, args []string) error {
						capture.record(cmd, args)
						return nil
					},
				}
				set.Flags().String("path", "", "")
				set.Flags().String("state", "", "")
				set.Flags().String("disposition", "", "")
				set.Flags().Bool("yolo", false, "")
				set.Flags().String("tls-mode", "", "")
				set.Flags().String("custom", "", "")
				root.AddCommand(set)
				sdk.RegisterComponentCommand(root)
				req, err := plugin.NewComponentSetRequest(params, passthrough...)
				if err != nil {
					t.Fatalf("NewComponentSetRequest() error = %v", err)
				}
				return req, capture
			},
			wantArgs: []string{"fcrepo", "distributed"},
			wantFlags: map[string]string{
				"path":        "/srv/site",
				"state":       "on",
				"disposition": "enabled",
				"yolo":        "true",
				"tls-mode":    "letsencrypt",
				"custom":      "x",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			contextName := saveRPCRoundTripContext(t)
			sdk := plugin.NewSDK(plugin.Metadata{Name: "isle"})
			req, capture := tt.configure(t, sdk, tt.argv)
			req.Context = contextName

			resp := executeRoundTripRPC(t, sdk, req)
			if !resp.OK {
				t.Fatalf("expected OK response, got %+v", resp)
			}
			if !reflect.DeepEqual(capture.args, tt.wantArgs) {
				t.Fatalf("args = %#v, want %#v", capture.args, tt.wantArgs)
			}
			if !reflect.DeepEqual(capture.flags, tt.wantFlags) {
				t.Fatalf("flags = %#v, want %#v", capture.flags, tt.wantFlags)
			}
			if capture.format != tt.wantFormat {
				t.Fatalf("format = %q, want %q", capture.format, tt.wantFormat)
			}
		})
	}
}

type rpcRoundTripCapture struct {
	args   []string
	flags  map[string]string
	format string
}

func (c *rpcRoundTripCapture) record(cmd *cobra.Command, args []string) {
	c.args = append([]string{}, args...)
	flags := map[string]string{}
	cmd.Flags().Visit(func(flag *pflag.Flag) {
		flags[flag.Name] = flag.Value.String()
	})
	c.flags = flags
}

type convergeRoundTripRunner struct {
	capture *rpcRoundTripCapture
	bind    func(*cobra.Command)
}

func (r convergeRoundTripRunner) BindFlags(cmd *cobra.Command) {
	if r.bind != nil {
		r.bind(cmd)
	}
}

func (r convergeRoundTripRunner) Run(cmd *cobra.Command, ctx *config.Context) error {
	r.capture.record(cmd, cmd.Flags().Args())
	return nil
}

type setRoundTripRunner struct {
	capture *rpcRoundTripCapture
	bind    func(*cobra.Command)
}

func (r setRoundTripRunner) BindFlags(cmd *cobra.Command) {
	if r.bind != nil {
		r.bind(cmd)
	}
}

func (r setRoundTripRunner) Run(cmd *cobra.Command, args []string, ctx *config.Context) error {
	r.capture.record(cmd, args)
	return nil
}

type validateRoundTripRunner struct {
	capture *rpcRoundTripCapture
	bind    func(*cobra.Command)
}

func (r validateRoundTripRunner) BindFlags(cmd *cobra.Command) {
	if r.bind != nil {
		r.bind(cmd)
	}
}

func (r validateRoundTripRunner) Run(cmd *cobra.Command, ctx *config.Context) ([]sitevalidate.Result, error) {
	r.capture.record(cmd, cmd.Flags().Args())
	return nil, nil
}

func saveRPCRoundTripContext(t *testing.T) string {
	t.Helper()

	projectDir := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	ctx := &config.Context{
		Name:           "museum",
		Site:           "museum",
		Plugin:         "isle",
		DockerHostType: config.ContextLocal,
		DockerSocket:   "/var/run/docker.sock",
		ProjectDir:     projectDir,
	}
	if err := config.SaveContext(ctx, true); err != nil {
		t.Fatalf("SaveContext() error = %v", err)
	}
	return ctx.Name
}

func executeRoundTripRPC(t *testing.T, sdk *plugin.SDK, req plugin.RPCRequest) plugin.RPCResponse {
	t.Helper()

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal(RPCRequest) error = %v", err)
	}
	cmd := sdk.GetRPCCommand()
	var stdout bytes.Buffer
	cmd.SetArgs([]string{})
	cmd.SetIn(bytes.NewReader(data))
	cmd.SetOut(&stdout)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("RPC command Execute() error = %v", err)
	}
	var resp plugin.RPCResponse
	if err := json.Unmarshal(stdout.Bytes(), &resp); err != nil {
		t.Fatalf("Unmarshal(RPCResponse) error = %v: %s", err, stdout.String())
	}
	return resp
}

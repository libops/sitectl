package plugin

import (
	"reflect"
	"strings"
	"testing"
)

func TestExtractRPCParamsFromArgsStopsPositionalsAtUnknownFlag(t *testing.T) {
	params, passthrough, err := ExtractRPCParamsFromArgs[ComponentSetParams]([]string{
		"--force",
		"fcrepo",
		"off",
	})
	if err != nil {
		t.Fatalf("ExtractRPCParamsFromArgs() error = %v", err)
	}
	if params.Name != "" || params.Disposition != "" {
		t.Fatalf("unexpected positional promotion: %+v", params)
	}
	wantPassthrough := []string{"--force", "fcrepo", "off"}
	if !reflect.DeepEqual(passthrough, wantPassthrough) {
		t.Fatalf("passthrough = %#v, want %#v", passthrough, wantPassthrough)
	}
}

func TestExtractRPCParamsFromArgsPromotesPositionalsBeforeUnknownFlag(t *testing.T) {
	params, passthrough, err := ExtractRPCParamsFromArgs[ComponentSetParams]([]string{
		"fcrepo",
		"off",
		"--force",
	})
	if err != nil {
		t.Fatalf("ExtractRPCParamsFromArgs() error = %v", err)
	}
	if params.Name != "fcrepo" || params.Disposition != "off" {
		t.Fatalf("params = %+v, want fcrepo/off", params)
	}
	wantPassthrough := []string{"--force"}
	if !reflect.DeepEqual(passthrough, wantPassthrough) {
		t.Fatalf("passthrough = %#v, want %#v", passthrough, wantPassthrough)
	}
}

func TestExtractRPCParamsFromArgsReportsSpecErrors(t *testing.T) {
	tests := []struct {
		name    string
		run     func() error
		wantErr string
	}{
		{
			name: "duplicate flag",
			run: func() error {
				_, _, err := ExtractRPCParamsFromArgs[duplicateRPCFlagParams](nil)
				return err
			},
			wantErr: `flag "path" is declared more than once`,
		},
		{
			name: "non-string positional",
			run: func() error {
				_, _, err := ExtractRPCParamsFromArgs[nonStringRPCPositionalParams]([]string{"fcrepo"})
				return err
			},
			wantErr: "positional field must be a string",
		},
		{
			name: "unsupported kind",
			run: func() error {
				_, _, err := ExtractRPCParamsFromArgs[unsupportedRPCKindParams](nil)
				return err
			},
			wantErr: "has unsupported RPC param kind slice",
		},
		{
			name: "invalid position",
			run: func() error {
				_, _, err := ExtractRPCParamsFromArgs[invalidRPCPositionParams](nil)
				return err
			},
			wantErr: `has invalid rpc_pos tag "first"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.run()
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want substring %q", err, tt.wantErr)
			}
		})
	}
}

func TestAppendRPCParamBuildersReportSpecErrors(t *testing.T) {
	tests := []struct {
		name    string
		run     func() error
		wantErr string
	}{
		{
			name: "flags",
			run: func() error {
				_, err := appendRPCParamFlags(nil, "", nil, unsupportedRPCKindParams{})
				return err
			},
			wantErr: "has unsupported RPC param kind slice",
		},
		{
			name: "positionals",
			run: func() error {
				_, err := appendRPCParamPositionals(nil, invalidRPCPositionParams{})
				return err
			},
			wantErr: `has invalid rpc_pos tag "first"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.run()
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want substring %q", err, tt.wantErr)
			}
		})
	}
}

func TestAppendRPCParamBuildersRejectAmbiguousStringValues(t *testing.T) {
	tests := []struct {
		name    string
		params  ComponentSetParams
		wantErr string
	}{
		{
			name:    "missing component name",
			params:  ComponentSetParams{},
			wantErr: "component name is required",
		},
		{
			name:    "whitespace positional",
			params:  ComponentSetParams{Name: "   "},
			wantErr: "component name is required",
		},
		{
			name:    "dash-prefixed positional",
			params:  ComponentSetParams{Name: "--fcrepo"},
			wantErr: "must not start with -",
		},
		{
			name:    "whitespace flag",
			params:  ComponentSetParams{Name: "fcrepo", Path: "   "},
			wantErr: "must not be only whitespace",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := componentSetArgs(tt.params, nil)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want substring %q", err, tt.wantErr)
			}
		})
	}
}

func TestAppendComponentTargetArgsEnforcesMethodApplicability(t *testing.T) {
	t.Run("reconcile accepts report and yolo", func(t *testing.T) {
		got, err := appendComponentTargetArgs(nil, "reconcile", []string{"reconcile"}, ComponentTargetParams{
			Report: true,
			Yolo:   true,
		})
		if err != nil {
			t.Fatalf("appendComponentTargetArgs() error = %v", err)
		}
		want := []string{"reconcile", "--report", "--yolo"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("args = %#v, want %#v", got, want)
		}
	})

	t.Run("describe rejects report", func(t *testing.T) {
		_, err := appendComponentTargetArgs(nil, "describe", []string{"describe"}, ComponentTargetParams{
			Report: true,
		})
		if err == nil {
			t.Fatal("expected method applicability error")
		}
		if !strings.Contains(err.Error(), "component.describe does not support ComponentTargetParams.Report") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("describe rejects yolo", func(t *testing.T) {
		_, err := appendComponentTargetArgs(nil, "describe", []string{"describe"}, ComponentTargetParams{
			Yolo: true,
		})
		if err == nil {
			t.Fatal("expected method applicability error")
		}
		if !strings.Contains(err.Error(), "component.describe does not support ComponentTargetParams.Yolo") {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

func TestComponentTargetRequestBuildersEnforceMethodApplicability(t *testing.T) {
	t.Run("describe rejects reconcile-only params", func(t *testing.T) {
		_, err := NewComponentDescribeRequest(ComponentTargetParams{Report: true})
		if err == nil {
			t.Fatal("expected method applicability error")
		}
		if !strings.Contains(err.Error(), "component.describe does not support ComponentTargetParams.Report") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("reconcile accepts reconcile-only params", func(t *testing.T) {
		if _, err := NewComponentReconcileRequest(ComponentTargetParams{Report: true, Yolo: true}); err != nil {
			t.Fatalf("NewComponentReconcileRequest() error = %v", err)
		}
	})
}

func TestRPCParamsAreSensitiveUsesPointerReceiverOnValueParams(t *testing.T) {
	if !rpcParamsAreSensitive(pointerReceiverSensitiveParams{Token: "secret"}) {
		t.Fatal("expected by-value params with pointer-receiver sensitivity marker to be sensitive")
	}
}

type duplicateRPCFlagParams struct {
	Path    string `rpc_flags:"path"`
	AltPath string `rpc_flags:"path"`
}

type nonStringRPCPositionalParams struct {
	Enabled bool `rpc_pos:"0"`
}

type unsupportedRPCKindParams struct {
	Names []string `rpc_flags:"name"`
}

type invalidRPCPositionParams struct {
	Name string `rpc_pos:"first"`
}

type pointerReceiverSensitiveParams struct {
	Token string
}

func (p *pointerReceiverSensitiveParams) HasSensitiveRPCParams() bool {
	return strings.TrimSpace(p.Token) != ""
}

package plugin

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	corecomponent "github.com/libops/sitectl/pkg/component"
	"github.com/libops/sitectl/pkg/config"
	"github.com/spf13/cobra"
)

func TestResolveServiceSetState(t *testing.T) {
	t.Parallel()

	def := corecomponent.Definition{
		Name:                "fcrepo",
		DefaultState:        corecomponent.StateOff,
		DefaultDisposition:  corecomponent.DispositionSuperseded,
		AllowedDispositions: []corecomponent.Disposition{corecomponent.DispositionEnabled, corecomponent.DispositionSuperseded, corecomponent.DispositionDistributed},
	}

	tests := []struct {
		name            string
		argDisposition  string
		stateFlag       string
		dispositionFlag string
		wantState       corecomponent.State
		wantDisposition corecomponent.Disposition
		wantErr         string
	}{
		{
			name:            "default disposition",
			wantState:       corecomponent.StateOff,
			wantDisposition: corecomponent.DispositionSuperseded,
		},
		{
			name:            "positional disposition",
			argDisposition:  "enabled",
			wantState:       corecomponent.StateOn,
			wantDisposition: corecomponent.DispositionEnabled,
		},
		{
			name:            "state flag maps through allowed dispositions",
			stateFlag:       "off",
			wantState:       corecomponent.StateOff,
			wantDisposition: corecomponent.DispositionSuperseded,
		},
		{
			name:            "disposition flag",
			dispositionFlag: "distributed",
			wantState:       corecomponent.StateOn,
			wantDisposition: corecomponent.DispositionDistributed,
		},
		{
			name:           "state conflicts with positional disposition",
			argDisposition: "enabled",
			stateFlag:      "off",
			wantErr:        "--state cannot be combined with a disposition",
		},
		{
			name:            "disposition flag conflicts with positional disposition",
			argDisposition:  "enabled",
			dispositionFlag: "distributed",
			wantErr:         "--disposition cannot be combined with a positional disposition",
		},
		{
			name:           "disallowed disposition",
			argDisposition: "triplet",
			wantErr:        "disposition \"triplet\" is not allowed",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			gotState, gotDisposition, err := resolveServiceSetState(def, tc.argDisposition, tc.stateFlag, tc.dispositionFlag)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("resolveServiceSetState() error = %v, want containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveServiceSetState() error = %v", err)
			}
			if gotState != tc.wantState || gotDisposition != tc.wantDisposition {
				t.Fatalf("resolveServiceSetState() = (%q, %q), want (%q, %q)", gotState, gotDisposition, tc.wantState, tc.wantDisposition)
			}
		})
	}
}

func TestServiceComponentSetDoesNotRegisterDrupalRootfsFlag(t *testing.T) {
	t.Parallel()

	component := testComposeServiceComponent(t, "fcrepo")
	registry := serviceComponentRegistry{
		sdk:         &SDK{Metadata: Metadata{Name: "isle"}},
		displayName: "ISLE",
		components:  []corecomponent.ComposeServiceComponent{component},
	}

	set := childCommand(registry.command(), "set")
	if set == nil {
		t.Fatal("expected set command")
	}
	if flag := set.Flags().Lookup("drupal-rootfs"); flag != nil {
		t.Fatalf("expected set command not to register --drupal-rootfs")
	}
}

func TestServiceComponentSetRegistersBoolFollowUpFlags(t *testing.T) {
	t.Parallel()

	component := testComposeServiceComponentWithFollowUps(t, "dev-mode", []corecomponent.FollowUpSpec{{
		Name:         "assistant",
		FlagName:     "assistant",
		DefaultValue: "false",
		BoolValue:    true,
		AppliesTo:    corecomponent.StateOn,
	}})
	registry := serviceComponentRegistry{
		sdk:         &SDK{Metadata: Metadata{Name: "wp"}},
		displayName: "WordPress",
		components:  []corecomponent.ComposeServiceComponent{component},
	}

	set := childCommand(registry.command(), "set")
	if set == nil {
		t.Fatal("expected set command")
	}
	flag := set.Flags().Lookup("assistant")
	if flag == nil {
		t.Fatal("expected --assistant flag")
	}
	if flag.Value.Type() != "bool" {
		t.Fatalf("expected --assistant to be bool, got %q", flag.Value.Type())
	}
}

func TestServiceComponentAssistantFlagImpliesEnabledDevMode(t *testing.T) {
	t.Parallel()

	def := corecomponent.Definition{Name: "dev-mode"}
	if !shouldEnableDevModeForAssistant(def, "", "", "", map[string]string{"assistant": "true"}) {
		t.Fatal("expected --assistant to imply enabled dev-mode")
	}
	if shouldEnableDevModeForAssistant(def, "disabled", "", "", map[string]string{"assistant": "true"}) {
		t.Fatal("expected explicit disposition to win over --assistant")
	}
	if shouldEnableDevModeForAssistant(corecomponent.Definition{Name: "ingress"}, "", "", "", map[string]string{"assistant": "true"}) {
		t.Fatal("expected --assistant to affect only dev-mode")
	}
}

func TestComponentRootfsFlagValueRejectsConflictingAliases(t *testing.T) {
	t.Parallel()

	cmd, codebaseRootfs, drupalRootfs := rootfsAliasCommand(t, "--codebase-rootfs", "app/rootfs", "--drupal-rootfs", "drupal/rootfs")
	_, err := componentRootfsFlagValue(cmd, codebaseRootfs, drupalRootfs)
	if err == nil {
		t.Fatal("expected conflicting rootfs alias error")
	}
	if !strings.Contains(err.Error(), "--codebase-rootfs and --drupal-rootfs cannot be combined") {
		t.Fatalf("unexpected error: %v", err)
	}

	cmd, codebaseRootfs, drupalRootfs = rootfsAliasCommand(t, "--codebase-rootfs", "app/rootfs", "--drupal-rootfs", "app/rootfs")
	got, err := componentRootfsFlagValue(cmd, codebaseRootfs, drupalRootfs)
	if err != nil {
		t.Fatalf("componentRootfsFlagValue() error = %v", err)
	}
	if got != "app/rootfs" {
		t.Fatalf("componentRootfsFlagValue() = %q, want app/rootfs", got)
	}
}

func TestRegisterServiceComponentsMergesMultipleCalls(t *testing.T) {
	sdk := NewSDK(Metadata{Name: "isle"})
	sdk.RegisterServiceComponents(ServiceComponentRegistryOptions{
		DisplayName: "ISLE",
		Components:  []corecomponent.ComposeServiceComponent{testComposeServiceComponent(t, "fcrepo")},
	})
	sdk.RegisterServiceComponents(ServiceComponentRegistryOptions{
		Components: []corecomponent.ComposeServiceComponent{
			testComposeServiceComponentFromYAML(t, "solr", []byte("services:\n  solr:\n    image: example/solr\n"), corecomponent.StateOn),
		},
	})

	req, err := NewComponentListRequest("")
	if err != nil {
		t.Fatalf("NewComponentListRequest() error = %v", err)
	}
	resp, err := sdk.handleRPC(&cobra.Command{Use: "rpc"}, req)
	if err != nil {
		t.Fatalf("handleRPC(component.list) error = %v", err)
	}
	if !resp.OK {
		t.Fatalf("component.list returned non-OK response: %+v", resp)
	}
	if !strings.Contains(resp.Output, "fcrepo") || !strings.Contains(resp.Output, "solr") {
		t.Fatalf("expected merged component catalog, got:\n%s", resp.Output)
	}
}

func TestRegisterServiceComponentsExposesTopLevelSetAndConverge(t *testing.T) {
	t.Parallel()

	sdk := NewSDK(Metadata{Name: "app"})
	sdk.RegisterServiceComponents(ServiceComponentRegistryOptions{
		DisplayName: "App",
		Components: []corecomponent.ComposeServiceComponent{
			testComposeServiceComponentFromYAML(t, "queue", []byte("services:\n  queue:\n    image: example/queue\n"), corecomponent.StateOn),
		},
	})

	metadata := sdk.discoveryMetadata()
	if !metadata.CanSet || !metadata.CanConverge {
		t.Fatalf("service component metadata = CanSet:%t CanConverge:%t, want both true", metadata.CanSet, metadata.CanConverge)
	}
}

func TestServiceComponentTopLevelSetFallsBackToComponentRPC(t *testing.T) {
	projectDir := t.TempDir()
	composePath := filepath.Join(projectDir, "docker-compose.yml")
	if err := os.WriteFile(composePath, []byte("services:\n  queue:\n    image: example/queue\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(compose) error = %v", err)
	}

	sdk := NewSDK(Metadata{Name: "app"})
	sdk.RegisterServiceComponents(ServiceComponentRegistryOptions{
		DisplayName: "App",
		Components: []corecomponent.ComposeServiceComponent{
			testComposeServiceComponentFromYAML(t, "queue", []byte("services:\n  queue:\n    image: example/queue\n"), corecomponent.StateOn),
		},
	})
	sdk.contextCache = &config.Context{
		Name:           "app-local",
		Plugin:         "app",
		DockerHostType: config.ContextLocal,
		ProjectDir:     projectDir,
	}

	req, err := NewSetRunRequest(SetRunParams{}, "queue", "disabled", "--yolo")
	if err != nil {
		t.Fatalf("NewSetRunRequest() error = %v", err)
	}
	resp, err := sdk.handleRPC(&cobra.Command{Use: "rpc"}, req)
	if err != nil {
		t.Fatalf("handleRPC(set.run) error = %v", err)
	}
	if !resp.OK {
		t.Fatalf("set.run response = %#v", resp)
	}
	data, err := os.ReadFile(composePath)
	if err != nil {
		t.Fatalf("ReadFile(compose) error = %v", err)
	}
	if strings.Contains(string(data), "queue:") {
		t.Fatalf("top-level set did not remove queue service:\n%s", data)
	}
}

func TestServiceComponentConvergePreservesValidDisabledState(t *testing.T) {
	projectDir := t.TempDir()
	composePath := filepath.Join(projectDir, "docker-compose.yml")
	if err := os.WriteFile(composePath, []byte("services:\n  app:\n    image: example/app\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(compose) error = %v", err)
	}

	sdk := NewSDK(Metadata{Name: "app"})
	sdk.RegisterServiceComponents(ServiceComponentRegistryOptions{
		DisplayName: "App",
		Components: []corecomponent.ComposeServiceComponent{
			testComposeServiceComponentFromYAML(t, "queue", []byte("services:\n  queue:\n    image: example/queue\n"), corecomponent.StateOn),
		},
	})
	sdk.contextCache = &config.Context{
		Name:           "app-local",
		Plugin:         "app",
		DockerHostType: config.ContextLocal,
		ProjectDir:     projectDir,
	}

	req, err := NewConvergeRunRequest(ConvergeRunParams{}, "--yolo")
	if err != nil {
		t.Fatalf("NewConvergeRunRequest() error = %v", err)
	}
	resp, err := sdk.handleRPC(&cobra.Command{Use: "rpc"}, req)
	if err != nil {
		t.Fatalf("handleRPC(converge.run) error = %v", err)
	}
	if !resp.OK {
		t.Fatalf("converge.run response = %#v", resp)
	}
	if !strings.Contains(resp.Output, "No component drift detected") {
		t.Fatalf("converge output = %q", resp.Output)
	}
	data, err := os.ReadFile(composePath)
	if err != nil {
		t.Fatalf("ReadFile(compose) error = %v", err)
	}
	if strings.Contains(string(data), "queue:") {
		t.Fatalf("converge restored an intentionally disabled service:\n%s", data)
	}
}

func TestReconcileCreateServiceComponentsDispatchesStates(t *testing.T) {
	tests := []struct {
		name          string
		componentName string
		initial       string
		composeYAML   string
		defaultState  corecomponent.State
		decisionState corecomponent.State
		wantContains  string
		wantNot       string
		wantErr       string
	}{
		{
			name:          "enables explicit on state",
			componentName: "cache",
			initial:       "services:\n  app:\n    image: example/app\n",
			composeYAML:   "services:\n  cache:\n    image: example/cache\n",
			defaultState:  corecomponent.StateOff,
			decisionState: corecomponent.StateOn,
			wantContains:  "cache:",
		},
		{
			name:          "disables explicit off state",
			componentName: "search",
			initial:       "services:\n  app:\n    image: example/app\n  search:\n    image: example/search\n",
			composeYAML:   "services:\n  search:\n    image: example/search\n",
			defaultState:  corecomponent.StateOn,
			decisionState: corecomponent.StateOff,
			wantNot:       "search:",
		},
		{
			name:          "uses component default state",
			componentName: "queue",
			initial:       "services:\n  app:\n    image: example/app\n",
			composeYAML:   "services:\n  queue:\n    image: example/queue\n",
			defaultState:  corecomponent.StateOn,
			wantContains:  "queue:",
		},
		{
			name:          "rejects unsupported state",
			componentName: "cache",
			initial:       "services:\n  app:\n    image: example/app\n",
			composeYAML:   "services:\n  cache:\n    image: example/cache\n",
			defaultState:  corecomponent.StateOn,
			decisionState: corecomponent.State("sideways"),
			wantErr:       `unsupported create component state "sideways"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			projectDir := t.TempDir()
			composePath := filepath.Join(projectDir, "docker-compose.yml")
			if err := os.WriteFile(composePath, []byte(tt.initial), 0o600); err != nil {
				t.Fatalf("WriteFile(compose) error = %v", err)
			}

			component := testComposeServiceComponentFromYAML(t, tt.componentName, []byte(tt.composeYAML), tt.defaultState)
			sdk := &SDK{}
			sdk.RegisterServiceComponents(ServiceComponentRegistryOptions{
				Components: []corecomponent.ComposeServiceComponent{component},
			})

			target := &config.Context{
				Name:           "local",
				DockerHostType: config.ContextLocal,
				ProjectDir:     projectDir,
			}
			err := sdk.reconcileCreateServiceComponents(context.Background(), target, map[string]corecomponent.ReviewDecision{
				tt.componentName: {State: tt.decisionState},
			})
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("reconcileCreateServiceComponents() error = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("reconcileCreateServiceComponents() error = %v", err)
			}

			data, err := os.ReadFile(composePath)
			if err != nil {
				t.Fatalf("ReadFile(compose) error = %v", err)
			}
			if tt.wantContains != "" && !strings.Contains(string(data), tt.wantContains) {
				t.Fatalf("expected compose to contain %q, got:\n%s", tt.wantContains, string(data))
			}
			if tt.wantNot != "" && strings.Contains(string(data), tt.wantNot) {
				t.Fatalf("expected compose not to contain %q, got:\n%s", tt.wantNot, string(data))
			}
		})
	}
}

func testComposeServiceComponent(t *testing.T, name string) corecomponent.ComposeServiceComponent {
	t.Helper()

	return testComposeServiceComponentFromYAML(t, name, readPluginFixture(t, "service-component-"+name+".yml"), corecomponent.StateOn)
}

func testComposeServiceComponentFromYAML(t *testing.T, name string, composeYAML []byte, defaultState corecomponent.State) corecomponent.ComposeServiceComponent {
	t.Helper()

	component, err := corecomponent.NewComposeServiceComponent(corecomponent.ComposeServiceComponentOptions{
		Name:         name,
		ComposeYAML:  composeYAML,
		DefaultState: defaultState,
	})
	if err != nil {
		t.Fatalf("NewComposeServiceComponent() error = %v", err)
	}
	return component
}

func testComposeServiceComponentWithFollowUps(t *testing.T, name string, followUps []corecomponent.FollowUpSpec) corecomponent.ComposeServiceComponent {
	t.Helper()

	component, err := corecomponent.NewComposeServiceComponent(corecomponent.ComposeServiceComponentOptions{
		Name:         name,
		ComposeYAML:  []byte("services:\n  app:\n    image: example/app\n"),
		DefaultState: corecomponent.StateOff,
		FollowUps:    followUps,
	})
	if err != nil {
		t.Fatalf("NewComposeServiceComponent() error = %v", err)
	}
	return component
}

func readPluginFixture(t *testing.T, name string) []byte {
	t.Helper()

	data, err := os.ReadFile(filepath.Join("testdata", "fixtures", name))
	if err != nil {
		t.Fatalf("ReadFile(plugin fixture %q) error = %v", name, err)
	}
	return data
}

func rootfsAliasCommand(t *testing.T, args ...string) (*cobra.Command, string, string) {
	t.Helper()

	var codebaseRootfs string
	var drupalRootfs string
	cmd := &cobra.Command{Use: "describe"}
	cmd.Flags().StringVar(&codebaseRootfs, "codebase-rootfs", "", "")
	cmd.Flags().StringVar(&drupalRootfs, "drupal-rootfs", "", "")
	if err := cmd.ParseFlags(args); err != nil {
		t.Fatalf("ParseFlags(%v) error = %v", args, err)
	}
	return cmd, codebaseRootfs, drupalRootfs
}

func childCommand(root *cobra.Command, name string) *cobra.Command {
	for _, child := range root.Commands() {
		if child.Name() == name {
			return child
		}
	}
	return nil
}

package component

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/libops/sitectl/pkg/config"
)

func TestDesiredStateRoundTripAndPlan(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	compose := "services:\n  web:\n    image: nginx:old\n"
	if err := os.WriteFile(filepath.Join(root, "compose.yaml"), []byte(compose), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := &config.Context{Name: "demo-local", Site: "demo", Plugin: "demo", ProjectDir: root, DockerHostType: config.ContextLocal}
	def := Definition{
		Name:                "web",
		AllowedDispositions: []Disposition{DispositionEnabled, DispositionDisabled},
		On: DomainSpec{Compose: YAMLStateSpec{Rules: []YAMLRule{{
			Files: []string{"compose.yaml"}, Op: OpSet, Path: "services.web.image", Value: "nginx:new",
		}}}},
	}
	state := NewDesiredState("demo")
	if err := state.Set(def, DispositionEnabled, map[string]string{"mode": "safe"}); err != nil {
		t.Fatal(err)
	}
	if err := SaveDesiredState(ctx, state); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadDesiredState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := BuildReconciliationPlan(ctx, root, loaded, DetectOptions{ComposeRoot: root}, def)
	if err != nil {
		t.Fatal(err)
	}
	if plan.InSync || !plan.Safe || len(plan.Components) != 1 || len(plan.Components[0].Changes) != 1 {
		t.Fatalf("unexpected plan: %#v", plan)
	}
}

func TestDesiredStateRejectsUnknownFieldsAndPluginMismatch(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	ctx := &config.Context{Plugin: "demo", ProjectDir: root, DockerHostType: config.ContextLocal}
	path, err := DesiredStateFile(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	data := strings.ReplaceAll(`apiVersion: sitectl.libops.io/v1alpha1
kind: SiteDesiredState
schema: 1
spec:
  plugin: other
  components: {}
  surprise: true
`, "\t", "  ")
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadDesiredState(ctx); err == nil || !strings.Contains(err.Error(), "field surprise not found") {
		t.Fatalf("LoadDesiredState() error = %v", err)
	}
}

func TestPlanRedactsSensitiveDetail(t *testing.T) {
	t.Parallel()
	got := redactPlanDetail("password expected hunter2")
	if strings.Contains(got, "hunter2") {
		t.Fatalf("sensitive detail was not redacted: %q", got)
	}
}

func TestPlanOutputRedactsSensitiveSettings(t *testing.T) {
	t.Parallel()
	plan := ReconciliationPlan{Components: []ComponentPlan{{
		Name: "example",
		Selection: ComponentSelection{Settings: map[string]string{
			"api-token": "do-not-print",
			"mode":      "safe",
		}},
	}}}
	var output bytes.Buffer
	if err := WriteReconciliationPlan(&output, plan, ReportFormatJSON); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "do-not-print") {
		t.Fatalf("sensitive setting was not redacted: %s", output.String())
	}
	if !strings.Contains(output.String(), `"mode": "safe"`) {
		t.Fatalf("non-sensitive setting missing from output: %s", output.String())
	}
}

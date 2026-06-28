package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/libops/sitectl/pkg/config"
	"github.com/libops/sitectl/pkg/plugin"
	"github.com/spf13/cobra"
)

func TestMaybeRunComposeReconcileSkipsCachedProject(t *testing.T) {
	restore := stubComposeReconcile(t)
	defer restore()

	var loadedSpec bool
	composeReconcileSpec = func(string) (plugin.CreateSpec, bool, error) {
		loadedSpec = true
		return plugin.CreateSpec{}, true, nil
	}
	composeReconcileHit = func(_ *config.Context, spec plugin.CreateSpec) (bool, error) {
		if !loadedSpec {
			t.Fatal("expected desired spec to load before cache lookup")
		}
		return true, nil
	}
	var inspected bool
	composeReconcileNeed = func(*config.Context, plugin.CreateSpec) (composeReconcileStatus, error) {
		inspected = true
		return statusWithFalse(conditionInitialized, "InitArtifactMissing", "missing"), nil
	}

	handled, err := maybeRunComposeReconcile(testComposeReconcileCommand(), testComposeReconcileContext())
	if err != nil {
		t.Fatalf("maybeRunComposeReconcile() error = %v", err)
	}
	if handled {
		t.Fatal("expected cached project to fall through to raw compose up")
	}
	if !loadedSpec {
		t.Fatal("expected cached project to load plugin create metadata")
	}
	if inspected {
		t.Fatal("expected cached project to skip the reconcile probe")
	}
}

func TestMaybeRunComposeReconcileCachesInstalledProject(t *testing.T) {
	restore := stubComposeReconcile(t)
	defer restore()

	var markedReason string
	composeReconcileNeed = func(*config.Context, plugin.CreateSpec) (composeReconcileStatus, error) {
		return statusWithTrue(conditionReconciled, "Observed"), nil
	}
	composeReconcileMark = func(_ *config.Context, status composeReconcileStatus, _ plugin.CreateSpec) error {
		markedReason = status.summary()
		return nil
	}

	handled, err := maybeRunComposeReconcile(testComposeReconcileCommand(), testComposeReconcileContext())
	if err != nil {
		t.Fatalf("maybeRunComposeReconcile() error = %v", err)
	}
	if handled {
		t.Fatal("expected installed project to fall through to raw compose up")
	}
	if markedReason != "conditions satisfied" {
		t.Fatalf("cache reason = %q", markedReason)
	}
}

func TestMaybeRunComposeReconcileRunsWhenProjectNeedsInstall(t *testing.T) {
	restore := stubComposeReconcile(t)
	defer restore()

	var ran bool
	var marked bool
	composeReconcileNeed = func(*config.Context, plugin.CreateSpec) (composeReconcileStatus, error) {
		return statusWithFalse(conditionInitialized, "InitArtifactMissing", "secret DB_ROOT_PASSWORD is missing"), nil
	}
	composeReconcileRun = func(_ *cobra.Command, _ *config.Context, decision composeReconcileDecision) error {
		ran = true
		if !decision.RunInit || decision.RunBuild {
			t.Fatalf("expected init-only reconcile, got init=%t build=%t", decision.RunInit, decision.RunBuild)
		}
		return nil
	}
	composeReconcileMark = func(_ *config.Context, status composeReconcileStatus, _ plugin.CreateSpec) error {
		marked = status.summary() == "secret DB_ROOT_PASSWORD is missing"
		return nil
	}

	var stderr bytes.Buffer
	cmd := testComposeReconcileCommand()
	cmd.SetErr(&stderr)
	handled, err := maybeRunComposeReconcile(cmd, testComposeReconcileContext())
	if err != nil {
		t.Fatalf("maybeRunComposeReconcile() error = %v", err)
	}
	if !handled {
		t.Fatal("expected reconcile to handle compose up")
	}
	if !ran {
		t.Fatal("expected plugin create wiring to run")
	}
	if !marked {
		t.Fatal("expected successful reconcile to be cached")
	}
	if !bytes.Contains(stderr.Bytes(), []byte("reconcile")) {
		t.Fatalf("expected reconcile message, got %q", stderr.String())
	}
}

func TestInspectComposeReconcileNeedUsesExplicitFilesAndImages(t *testing.T) {
	oldImageMissing := composeReconcileImageMissing
	oldVolumeMissing := composeReconcileVolumeMissing
	oldReadConfig := composeReconcileReadConfig
	oldUserID := composeReconcileUserID
	t.Cleanup(func() {
		composeReconcileImageMissing = oldImageMissing
		composeReconcileVolumeMissing = oldVolumeMissing
		composeReconcileReadConfig = oldReadConfig
		composeReconcileUserID = oldUserID
	})

	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "secrets-present"), []byte("secret"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	composeReconcileImageMissing = func(image string) bool {
		return image == "libops/wp:missing"
	}
	composeReconcileVolumeMissing = func(volume string) bool {
		return volume == "wp_wordpress-uploads"
	}
	composeReconcileReadConfig = func(*config.Context) (composeConfigDocument, error) {
		return composeConfigDocument{
			Name: "wp",
			Volumes: map[string]composeConfigVolume{
				"wordpress-uploads": {Name: "wp_wordpress-uploads"},
			},
		}, nil
	}
	composeReconcileUserID = func() string { return "1000" }

	probe, err := inspectComposeReconcileNeed(&config.Context{ProjectDir: tmpDir}, plugin.CreateSpec{
		DockerComposeInit:  []string{"init"},
		DockerComposeBuild: []string{"build"},
		InitArtifacts: []plugin.InitArtifact{
			{Path: "secrets-present"},
			{Path: "secrets-missing"},
			{Path: "certs/UID", ValueFrom: plugin.InitArtifactValueFromHostUID},
		},
		InitVolumes: []plugin.InitVolume{
			{Name: "wordpress-uploads"},
		},
		Images: []plugin.ComposeImageSpec{{
			Service:     "wp",
			Image:       "libops/wp:missing",
			BuildPolicy: plugin.BuildPolicyIfNotPresent,
		}},
	})
	if err != nil {
		t.Fatalf("inspectComposeReconcileNeed() error = %v", err)
	}
	if !probe.needsInit() || !probe.needsBuild() {
		t.Fatalf("expected init and build to be needed, got %+v", probe)
	}
	summary := probe.summary()
	if !strings.Contains(summary, "secrets-missing is missing") ||
		!strings.Contains(summary, "certs/UID is missing") ||
		!strings.Contains(summary, "volume wp_wordpress-uploads is missing") ||
		!strings.Contains(summary, "image libops/wp:missing is missing") {
		t.Fatalf("unexpected probe summary %q", summary)
	}
}

func TestInspectComposeReconcileNeedChecksHostUIDFileValue(t *testing.T) {
	oldUserID := composeReconcileUserID
	t.Cleanup(func() {
		composeReconcileUserID = oldUserID
	})

	tmpDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmpDir, "certs"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "certs", "UID"), []byte("1000\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	composeReconcileUserID = func() string { return "1001" }

	probe, err := inspectComposeReconcileNeed(&config.Context{ProjectDir: tmpDir}, plugin.CreateSpec{
		DockerComposeInit: []string{"init"},
		InitArtifacts: []plugin.InitArtifact{
			{Path: "certs/UID", ValueFrom: plugin.InitArtifactValueFromHostUID},
		},
	})
	if err != nil {
		t.Fatalf("inspectComposeReconcileNeed() error = %v", err)
	}
	if !probe.needsInit() || probe.needsBuild() {
		t.Fatalf("expected init-only probe, got %+v", probe)
	}
	if !strings.Contains(probe.summary(), "certs/UID does not match host uid 1001") {
		t.Fatalf("unexpected probe summary %q", probe.summary())
	}
}

func TestInspectComposeReconcileNeedBuildsWhenBuildArgsOverrideExists(t *testing.T) {
	oldImageMissing := composeReconcileImageMissing
	t.Cleanup(func() {
		composeReconcileImageMissing = oldImageMissing
	})

	tmpDir := t.TempDir()
	override := []byte("services:\n  wp:\n    build:\n      args:\n        BASE_IMAGE: libops/wp:custom\n")
	if err := os.WriteFile(filepath.Join(tmpDir, plugin.ComposeImageOverrideFile), override, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	composeReconcileImageMissing = func(string) bool { return false }

	probe, err := inspectComposeReconcileNeed(&config.Context{ProjectDir: tmpDir}, plugin.CreateSpec{
		DockerComposeBuild: []string{"build"},
		Images: []plugin.ComposeImageSpec{{
			Service:     "wp",
			Image:       "libops/wp:nginx-1.30.3-php84",
			BuildPolicy: plugin.BuildPolicyIfNotPresent,
		}},
	})
	if err != nil {
		t.Fatalf("inspectComposeReconcileNeed() error = %v", err)
	}
	if !probe.needsBuild() || probe.needsInit() {
		t.Fatalf("expected build-only probe, got %+v", probe)
	}
	if !strings.Contains(probe.summary(), "build args for wp need applying") {
		t.Fatalf("unexpected probe summary %q", probe.summary())
	}
}

func TestInspectComposeReconcileNeedSkipsBuildForImageOverride(t *testing.T) {
	oldImageMissing := composeReconcileImageMissing
	t.Cleanup(func() {
		composeReconcileImageMissing = oldImageMissing
	})

	tmpDir := t.TempDir()
	override := []byte("services:\n  wp:\n    image: libops/wp:custom@sha256:abc\n")
	if err := os.WriteFile(filepath.Join(tmpDir, plugin.ComposeImageOverrideFile), override, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	composeReconcileImageMissing = func(string) bool { return true }

	probe, err := inspectComposeReconcileNeed(&config.Context{ProjectDir: tmpDir}, plugin.CreateSpec{
		DockerComposeBuild: []string{"build"},
		Images: []plugin.ComposeImageSpec{{
			Service:     "wp",
			Image:       "libops/wp:nginx-1.30.3-php84",
			BuildPolicy: plugin.BuildPolicyIfNotPresent,
		}},
	})
	if err != nil {
		t.Fatalf("inspectComposeReconcileNeed() error = %v", err)
	}
	if probe.needsInit() || probe.needsBuild() {
		t.Fatalf("expected image override to skip build probe, got %+v", probe)
	}
}

func stubComposeReconcile(t *testing.T) func() {
	t.Helper()
	oldSpec := composeReconcileSpec
	oldNeed := composeReconcileNeed
	oldRun := composeReconcileRun
	oldHit := composeReconcileHit
	oldMark := composeReconcileMark
	oldImageMissing := composeReconcileImageMissing
	oldVolumeMissing := composeReconcileVolumeMissing
	oldReadConfig := composeReconcileReadConfig
	oldUserID := composeReconcileUserID
	composeReconcileSpec = func(string) (plugin.CreateSpec, bool, error) {
		return plugin.CreateSpec{Name: "default", Default: true, DockerComposeInit: []string{"init"}, DockerComposeUp: []string{"up"}}, true, nil
	}
	composeReconcileNeed = func(*config.Context, plugin.CreateSpec) (composeReconcileStatus, error) {
		return statusWithTrue(conditionReconciled, "Observed"), nil
	}
	composeReconcileRun = func(*cobra.Command, *config.Context, composeReconcileDecision) error { return nil }
	composeReconcileHit = func(*config.Context, plugin.CreateSpec) (bool, error) { return false, nil }
	composeReconcileMark = func(*config.Context, composeReconcileStatus, plugin.CreateSpec) error { return nil }
	composeReconcileImageMissing = func(string) bool { return false }
	composeReconcileVolumeMissing = func(string) bool { return false }
	composeReconcileReadConfig = func(*config.Context) (composeConfigDocument, error) {
		return composeConfigDocument{}, nil
	}
	composeReconcileUserID = func() string { return "1000" }
	return func() {
		composeReconcileSpec = oldSpec
		composeReconcileNeed = oldNeed
		composeReconcileRun = oldRun
		composeReconcileHit = oldHit
		composeReconcileMark = oldMark
		composeReconcileImageMissing = oldImageMissing
		composeReconcileVolumeMissing = oldVolumeMissing
		composeReconcileReadConfig = oldReadConfig
		composeReconcileUserID = oldUserID
	}
}

func statusWithTrue(conditionType, reason string) composeReconcileStatus {
	return composeReconcileStatus{Conditions: []composeReconcileCondition{{
		Type:   conditionType,
		Status: conditionStatusTrue,
		Reason: reason,
	}}}
}

func statusWithFalse(conditionType, reason, message string) composeReconcileStatus {
	return composeReconcileStatus{Conditions: []composeReconcileCondition{{
		Type:    conditionType,
		Status:  conditionStatusFalse,
		Reason:  reason,
		Message: message,
	}}}
}

func testComposeReconcileCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "compose"}
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetIn(bytes.NewReader(nil))
	return cmd
}

func testComposeReconcileContext() *config.Context {
	return &config.Context{
		Name:           ".",
		Plugin:         "wp",
		DockerHostType: config.ContextLocal,
		ProjectDir:     "/tmp/wp",
	}
}

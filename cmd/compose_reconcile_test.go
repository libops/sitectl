package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
		if !decision.RunInit || !decision.RunBuild {
			t.Fatalf("expected init/build reconcile, got init=%t build=%t", decision.RunInit, decision.RunBuild)
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

func TestComposeReconcileDecisionForceBypassesCache(t *testing.T) {
	restore := stubComposeReconcile(t)
	defer restore()

	var inspected bool
	composeReconcileHit = func(*config.Context, plugin.CreateSpec) (bool, error) {
		t.Fatal("force should bypass cache lookup")
		return true, nil
	}
	composeReconcileNeed = func(*config.Context, plugin.CreateSpec) (composeReconcileStatus, error) {
		inspected = true
		return statusWithTrue(conditionReconciled, "Observed"), nil
	}

	decision, err := composeReconcileDecisionForContextWithOptions(testComposeReconcileContext(), composeReconcileOptions{Force: true})
	if err != nil {
		t.Fatalf("composeReconcileDecisionForContextWithOptions() error = %v", err)
	}
	if !inspected {
		t.Fatal("expected force to inspect current state")
	}
	if !decision.Needed || decision.RunInit || !decision.RunBuild {
		t.Fatalf("expected forced build/up decision, got %+v", decision)
	}
	if decision.Reason != "forced" {
		t.Fatalf("reason = %q, want forced", decision.Reason)
	}
}

func TestComposeReconcileDecisionAlwaysBuildBypassesCache(t *testing.T) {
	restore := stubComposeReconcile(t)
	defer restore()

	composeReconcileSpec = func(string) (plugin.CreateSpec, bool, error) {
		return plugin.CreateSpec{
			DockerComposeBuild: []string{"docker compose build"},
			Images: []plugin.ComposeImageSpec{{
				Service:     "app",
				Image:       "libops/app:1",
				BuildPolicy: plugin.BuildPolicyAlways,
			}},
		}, true, nil
	}
	composeReconcileHit = func(*config.Context, plugin.CreateSpec) (bool, error) {
		t.Fatal("BuildPolicyAlways must bypass the reconcile cache")
		return true, nil
	}
	composeReconcileNeed = func(*config.Context, plugin.CreateSpec) (composeReconcileStatus, error) {
		return statusWithFalse(conditionImagesAvailable, "BuildPolicyAlways", "app requires a build"), nil
	}

	decision, err := composeReconcileDecisionForContext(testComposeReconcileContext())
	if err != nil {
		t.Fatalf("composeReconcileDecisionForContext() error = %v", err)
	}
	if !decision.Needed || !decision.RunBuild {
		t.Fatalf("cached Always spec must still build, got %+v", decision)
	}
}

func TestShouldAutoReconcileComposeUpPreservesCustomizedStarts(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		args []string
		want bool
	}{
		{name: "plain", args: []string{"up"}, want: true},
		{name: "normalized defaults", args: []string{"up", "-d", "--remove-orphans"}, want: true},
		{name: "translated local daemon", args: []string{"up", "-d", "--remove-orphans", "--no-build"}, want: true},
		{name: "wait flag", args: []string{"up", "--wait"}},
		{name: "profile", args: []string{"up", "--profile", "assistant"}},
		{name: "selected service", args: []string{"up", "-d", "drupal"}},
		{name: "build requested", args: []string{"up", "--build"}},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := shouldAutoReconcileComposeUp(test.args); got != test.want {
				t.Fatalf("shouldAutoReconcileComposeUp(%q) = %t, want %t", test.args, got, test.want)
			}
		})
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

func TestResetComposeReconcileInitStateRemovesDeclaredFilesAndVolumes(t *testing.T) {
	restore := stubComposeReconcile(t)
	defer restore()

	tmpDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmpDir, "secrets"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "secrets", "DB_ROOT_PASSWORD"), []byte("secret"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	composeReconcileReadConfig = func(*config.Context) (composeConfigDocument, error) {
		return composeConfigDocument{
			Name: "wp",
			Volumes: map[string]composeConfigVolume{
				"mariadb-data": {Name: "wp_mariadb-data"},
			},
		}, nil
	}
	var removedVolume string
	composeReconcileVolumeRemove = func(volume string) error {
		removedVolume = volume
		return nil
	}

	removed, err := resetComposeReconcileInitState(&config.Context{ProjectDir: tmpDir}, plugin.CreateSpec{
		InitArtifacts: []plugin.InitArtifact{{Path: "secrets/DB_ROOT_PASSWORD"}},
		InitVolumes:   []plugin.InitVolume{{Name: "mariadb-data"}},
	})
	if err != nil {
		t.Fatalf("resetComposeReconcileInitState() error = %v", err)
	}
	if fileExists(filepath.Join(tmpDir, "secrets", "DB_ROOT_PASSWORD")) {
		t.Fatal("expected declared init file to be removed")
	}
	if removedVolume != "wp_mariadb-data" {
		t.Fatalf("removed volume = %q", removedVolume)
	}
	if strings.Join(removed, ",") != "file secrets/DB_ROOT_PASSWORD,volume wp_mariadb-data" {
		t.Fatalf("removed = %#v", removed)
	}
}

func TestEnsureComposeReconcileInitArtifactDirsCreatesParents(t *testing.T) {
	tmpDir := t.TempDir()
	ctx := &config.Context{ProjectDir: tmpDir}
	spec := plugin.CreateSpec{
		InitArtifacts: []plugin.InitArtifact{
			{Path: "secrets/DB_ROOT_PASSWORD"},
			{Path: "certs/UID", ValueFrom: plugin.InitArtifactValueFromHostUID},
			{Path: "ROOT_FILE"},
		},
	}

	if err := ensureComposeReconcileInitArtifactDirs(ctx, spec); err != nil {
		t.Fatalf("ensureComposeReconcileInitArtifactDirs() error = %v", err)
	}
	for _, dir := range []string{"secrets", "certs"} {
		if info, err := os.Stat(filepath.Join(tmpDir, dir)); err != nil || !info.IsDir() {
			t.Fatalf("expected %s directory to exist, info=%v err=%v", dir, info, err)
		}
	}
	if _, err := os.Stat(filepath.Join(tmpDir, "ROOT_FILE")); !os.IsNotExist(err) {
		t.Fatalf("expected root artifact file not to be created, err=%v", err)
	}
}

func TestComposeReconcileCacheExpiresAfterTTL(t *testing.T) {
	oldHost := composeReconcileHost
	oldNow := composeReconcileNow
	oldUserID := composeReconcileUserID
	t.Cleanup(func() {
		composeReconcileHost = oldHost
		composeReconcileNow = oldNow
		composeReconcileUserID = oldUserID
	})

	t.Setenv("HOME", t.TempDir())
	tmpDir := t.TempDir()
	ctx := &config.Context{ProjectDir: tmpDir, Plugin: "wp"}
	spec := plugin.CreateSpec{Name: "default"}
	composeReconcileHost = func() (string, error) { return "test-host", nil }
	composeReconcileUserID = func() string { return "1000" }
	base := composeReconcileNow()
	composeReconcileNow = func() time.Time { return base.Add(-composeReconcileCacheTTL - time.Minute) }
	if err := markComposeReconcileChecked(ctx, statusWithTrue(conditionReconciled, "Observed"), spec); err != nil {
		t.Fatalf("markComposeReconcileChecked() error = %v", err)
	}

	composeReconcileNow = func() time.Time { return base }
	checked, err := composeReconcileChecked(ctx, spec)
	if err != nil {
		t.Fatalf("composeReconcileChecked() error = %v", err)
	}
	if checked {
		t.Fatal("expected expired cache entry to miss")
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
	oldClear := composeReconcileClear
	oldReset := composeReconcileReset
	oldImageMissing := composeReconcileImageMissing
	oldVolumeMissing := composeReconcileVolumeMissing
	oldVolumeRemove := composeReconcileVolumeRemove
	oldRemoveFile := composeReconcileRemoveFile
	oldReadConfig := composeReconcileReadConfig
	oldUserID := composeReconcileUserID
	composeReconcileSpec = func(string) (plugin.CreateSpec, bool, error) {
		return plugin.CreateSpec{Name: "default", Default: true, DockerComposeInit: []string{"init"}, DockerComposeBuild: []string{"build"}, DockerComposeUp: []string{"up"}}, true, nil
	}
	composeReconcileNeed = func(*config.Context, plugin.CreateSpec) (composeReconcileStatus, error) {
		return statusWithTrue(conditionReconciled, "Observed"), nil
	}
	composeReconcileRun = func(*cobra.Command, *config.Context, composeReconcileDecision) error { return nil }
	composeReconcileHit = func(*config.Context, plugin.CreateSpec) (bool, error) { return false, nil }
	composeReconcileMark = func(*config.Context, composeReconcileStatus, plugin.CreateSpec) error { return nil }
	composeReconcileClear = func(*config.Context, plugin.CreateSpec) error { return nil }
	composeReconcileReset = resetComposeReconcileInitState
	composeReconcileImageMissing = func(string) bool { return false }
	composeReconcileVolumeMissing = func(string) bool { return false }
	composeReconcileVolumeRemove = func(string) error { return nil }
	composeReconcileRemoveFile = os.Remove
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
		composeReconcileClear = oldClear
		composeReconcileReset = oldReset
		composeReconcileImageMissing = oldImageMissing
		composeReconcileVolumeMissing = oldVolumeMissing
		composeReconcileVolumeRemove = oldVolumeRemove
		composeReconcileRemoveFile = oldRemoveFile
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

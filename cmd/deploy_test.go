package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/libops/sitectl/pkg/config"
	"github.com/libops/sitectl/pkg/plugin"
	"github.com/spf13/cobra"
)

func TestRunContextComposeUpRunsReconcileBeforeDocker(t *testing.T) {
	restore := stubComposeReconcile(t)
	defer restore()

	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "docker-compose.yml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	var ran bool
	composeReconcileNeed = func(*config.Context, plugin.CreateSpec) (composeReconcileStatus, error) {
		return statusWithFalse(conditionInitialized, "InitArtifactMissing", "secret DB_ROOT_PASSWORD is missing"), nil
	}
	composeReconcileRun = func(_ *cobra.Command, _ *config.Context, decision composeReconcileDecision) error {
		ran = decision.RunInit && decision.RunBuild
		return nil
	}

	ctx := config.Context{
		Name:           "wp",
		Plugin:         "wp",
		DockerHostType: config.ContextLocal,
		ProjectDir:     tmpDir,
	}
	if err := runContextCompose(testComposeReconcileCommand(), ctx, []string{"up", "-d", "--remove-orphans"}); err != nil {
		t.Fatalf("runContextCompose() error = %v", err)
	}
	if !ran {
		t.Fatal("expected reconcile to handle compose up")
	}
}

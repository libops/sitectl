package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/libops/sitectl/pkg/config"
	"github.com/libops/sitectl/pkg/plugin"
	"github.com/spf13/cobra"
)

func TestRunGitUpdateUsesRepositoryUpstream(t *testing.T) {
	command := config.Context{}.GitSyncShellCommand("")
	for _, want := range []string{
		"rev-parse --abbrev-ref --symbolic-full-name '@{u}'",
		"Skipping git sync: branch '$current_branch' has no upstream",
		"git pull --ff-only",
	} {
		if !strings.Contains(command, want) {
			t.Fatalf("expected git sync command to contain %q, got:\n%s", want, command)
		}
	}
}

func TestRunGitUpdateBranchOverrideCommand(t *testing.T) {
	command := config.Context{}.GitSyncShellCommand("release")
	if !strings.Contains(command, "git_branch=release") || !strings.Contains(command, "git fetch --prune --no-tags") || !strings.Contains(command, "git merge --ff-only") {
		t.Fatalf("expected branch override command, got:\n%s", command)
	}
}

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

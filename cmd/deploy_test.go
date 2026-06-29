package cmd

import (
	"strings"
	"testing"

	"github.com/libops/sitectl/pkg/config"
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
	if !strings.Contains(command, "git_branch=release") || !strings.Contains(command, "git checkout \"$git_branch\"") {
		t.Fatalf("expected branch override command, got:\n%s", command)
	}
}

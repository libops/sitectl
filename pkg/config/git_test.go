package config

import (
	"strings"
	"testing"
)

func TestGitSyncShellCommandUsesConfiguredRemoteAndBranch(t *testing.T) {
	got := GitSyncShellCommand("")
	for _, want := range []string{
		"upstream=$(git rev-parse --abbrev-ref --symbolic-full-name '@{u}'",
		"Skipping git sync: branch '$current_branch' has no upstream",
		"git pull --ff-only",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("GitSyncShellCommand() missing %q:\n%s", want, got)
		}
	}
}

func TestGitSyncShellCommandBranchOverride(t *testing.T) {
	got := GitSyncShellCommand("hotfix")
	if !strings.Contains(got, "git_branch=hotfix") {
		t.Fatalf("expected branch override in command, got:\n%s", got)
	}
	if !strings.Contains(got, "git checkout \"$git_branch\"") {
		t.Fatalf("expected branch checkout in command, got:\n%s", got)
	}
}

func TestContextGitSyncShellCommandUsesRepoUpstream(t *testing.T) {
	got := (Context{}).GitSyncShellCommand("")
	if !strings.Contains(got, "git pull --ff-only") {
		t.Fatalf("expected upstream pull command, got:\n%s", got)
	}
}

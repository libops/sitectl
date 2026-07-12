package config

import (
	"os"
	"os/exec"
	"path/filepath"
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
	for _, want := range []string{"git fetch --prune --no-tags", "git checkout \"$git_branch\"", "git merge --ff-only"} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected branch sync command to contain %q, got:\n%s", want, got)
		}
	}
}

func TestGitSyncRefShellCommandFetchesSequentialRefs(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	seed := filepath.Join(root, "seed")
	checkout := filepath.Join(root, "checkout")
	gitTestRun(t, root, "init", "--bare", remote)
	gitTestRun(t, root, "init", "-b", "main", seed)
	gitTestRun(t, seed, "config", "user.name", "sitectl test")
	gitTestRun(t, seed, "config", "user.email", "sitectl@example.invalid")
	if err := os.WriteFile(filepath.Join(seed, "version.txt"), []byte("main-one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTestRun(t, seed, "add", "version.txt")
	gitTestRun(t, seed, "commit", "-m", "main one")
	gitTestRun(t, seed, "remote", "add", "origin", remote)
	gitTestRun(t, seed, "push", "-u", "origin", "main")

	gitTestRun(t, seed, "checkout", "-b", "feature")
	if err := os.WriteFile(filepath.Join(seed, "version.txt"), []byte("pull-request\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTestRun(t, seed, "commit", "-am", "pull request")
	pullCommit := strings.TrimSpace(gitTestRun(t, seed, "rev-parse", "HEAD"))
	gitTestRun(t, seed, "push", "origin", "HEAD:refs/pull/7/head")

	gitTestRun(t, seed, "checkout", "main")
	if err := os.WriteFile(filepath.Join(seed, "version.txt"), []byte("main-two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTestRun(t, seed, "commit", "-am", "main two")
	mainCommit := strings.TrimSpace(gitTestRun(t, seed, "rev-parse", "HEAD"))
	gitTestRun(t, seed, "push", "origin", "main")
	gitTestRun(t, root, "clone", "--branch", "main", remote, checkout)

	runGitSyncShell(t, checkout, GitSyncRefShellCommand("refs/pull/7/head"))
	if got := strings.TrimSpace(gitTestRun(t, checkout, "rev-parse", "HEAD")); got != pullCommit {
		t.Fatalf("pull-request checkout = %s, want %s", got, pullCommit)
	}
	if got := strings.TrimSpace(gitTestRun(t, checkout, "branch", "--show-current")); got != "" {
		t.Fatalf("exact ref checkout remained on branch %q", got)
	}

	runGitSyncShell(t, checkout, GitSyncRefShellCommand("main"))
	if got := strings.TrimSpace(gitTestRun(t, checkout, "rev-parse", "HEAD")); got != mainCommit {
		t.Fatalf("main ref checkout = %s, want %s", got, mainCommit)
	}
}

func gitTestRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func runGitSyncShell(t *testing.T, dir, script string) {
	t.Helper()
	cmd := exec.Command("bash", "-lc", script)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git sync script: %v\n%s\n%s", err, out, script)
	}
}

func TestContextGitSyncShellCommandUsesRepoUpstream(t *testing.T) {
	got := (Context{}).GitSyncShellCommand("")
	if !strings.Contains(got, "git pull --ff-only") {
		t.Fatalf("expected upstream pull command, got:\n%s", got)
	}
}

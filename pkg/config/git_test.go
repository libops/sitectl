package config

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestSyncGitCheckoutRunsExplicitBranchOperationsInOrder(t *testing.T) {
	t.Parallel()

	branch := "release;touch-not-executed"
	remoteRef := "refs/remotes/upstream/" + branch
	commit := "0123456789abcdef0123456789abcdef01234567"
	expected := []expectedGitCommand{
		{quiet: true, args: []string{"rev-parse", "--is-inside-work-tree"}, output: "true\n"},
		{quiet: true, args: []string{"status", "--porcelain"}},
		{quiet: true, args: []string{"rev-parse", "--abbrev-ref", "HEAD"}, output: "main\n"},
		{quiet: true, args: []string{"check-ref-format", "--branch", branch}},
		{quiet: true, args: []string{"show-ref", "--verify", "--quiet", "refs/heads/" + branch}},
		{quiet: true, args: []string{"config", "--get", "branch." + branch + ".remote"}, output: "upstream\n"},
		{args: []string{"fetch", "--prune", "--no-tags", "--", "upstream", "refs/heads/" + branch + ":" + remoteRef}},
		{quiet: true, args: []string{"rev-parse", "--verify", remoteRef + "^{commit}"}, output: commit},
		{quiet: true, args: []string{"merge-base", "--is-ancestor", "refs/heads/" + branch, remoteRef}},
		{args: []string{"checkout", "-B", branch, remoteRef, "--"}},
	}
	run, assertComplete := newExpectedGitRunner(t, expected)
	var stdout bytes.Buffer
	if err := syncGitCheckout(context.Background(), &stdout, branch, run); err != nil {
		t.Fatalf("syncGitCheckout() error = %v", err)
	}
	assertComplete()
	if got, want := stdout.String(), "Fetching branch "+branch+" from upstream\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestSyncGitCheckoutCreatesMissingTrackingBranchFromOnlyRemote(t *testing.T) {
	t.Parallel()

	notFound := errors.New("not found")
	commit := "0123456789abcdef0123456789abcdef01234567"
	expected := []expectedGitCommand{
		{quiet: true, args: []string{"rev-parse", "--is-inside-work-tree"}, output: "true"},
		{quiet: true, args: []string{"status", "--porcelain"}},
		{quiet: true, args: []string{"rev-parse", "--abbrev-ref", "HEAD"}, output: "HEAD"},
		{quiet: true, args: []string{"check-ref-format", "--branch", "hotfix"}},
		{quiet: true, args: []string{"show-ref", "--verify", "--quiet", "refs/heads/hotfix"}, err: notFound},
		{quiet: true, args: []string{"config", "--get", "branch.hotfix.remote"}, err: notFound},
		{quiet: true, args: []string{"remote", "get-url", "origin"}, err: notFound},
		{quiet: true, args: []string{"remote"}, output: "upstream\n"},
		{args: []string{"fetch", "--prune", "--no-tags", "--", "upstream", "refs/heads/hotfix:refs/remotes/upstream/hotfix"}},
		{quiet: true, args: []string{"rev-parse", "--verify", "refs/remotes/upstream/hotfix^{commit}"}, output: commit},
		{args: []string{"checkout", "--track", "-b", "hotfix", "refs/remotes/upstream/hotfix"}},
	}
	run, assertComplete := newExpectedGitRunner(t, expected)
	if err := syncGitCheckout(context.Background(), io.Discard, "hotfix", run); err != nil {
		t.Fatalf("syncGitCheckout() error = %v", err)
	}
	assertComplete()
}

func TestSyncGitCheckoutRejectsNonFastForwardBeforeWorktreeMutation(t *testing.T) {
	t.Parallel()

	nonFastForward := errors.New("not an ancestor")
	commit := "0123456789abcdef0123456789abcdef01234567"
	expected := []expectedGitCommand{
		{quiet: true, args: []string{"rev-parse", "--is-inside-work-tree"}, output: "true"},
		{quiet: true, args: []string{"status", "--porcelain"}},
		{quiet: true, args: []string{"rev-parse", "--abbrev-ref", "HEAD"}, output: "main"},
		{quiet: true, args: []string{"check-ref-format", "--branch", "release"}},
		{quiet: true, args: []string{"show-ref", "--verify", "--quiet", "refs/heads/release"}},
		{quiet: true, args: []string{"config", "--get", "branch.release.remote"}, output: "release-origin"},
		{args: []string{"fetch", "--prune", "--no-tags", "--", "release-origin", "refs/heads/release:refs/remotes/release-origin/release"}},
		{quiet: true, args: []string{"rev-parse", "--verify", "refs/remotes/release-origin/release^{commit}"}, output: commit},
		{quiet: true, args: []string{"merge-base", "--is-ancestor", "refs/heads/release", "refs/remotes/release-origin/release"}, err: nonFastForward},
	}
	run, assertComplete := newExpectedGitRunner(t, expected)
	err := syncGitCheckout(context.Background(), io.Discard, "release", run)
	if !errors.Is(err, nonFastForward) || !strings.Contains(err.Error(), "refusing non-fast-forward") {
		t.Fatalf("syncGitCheckout() error = %v, want pre-mutation fast-forward refusal", err)
	}
	assertComplete()
}

func TestSyncGitCheckoutPullsCurrentUpstreamFFOnly(t *testing.T) {
	t.Parallel()

	expected := []expectedGitCommand{
		{quiet: true, args: []string{"rev-parse", "--is-inside-work-tree"}, output: "true"},
		{quiet: true, args: []string{"status", "--porcelain"}},
		{quiet: true, args: []string{"rev-parse", "--abbrev-ref", "HEAD"}, output: "main"},
		{quiet: true, args: []string{"rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}"}, output: "origin/main"},
		{args: []string{"pull", "--ff-only"}},
	}
	run, assertComplete := newExpectedGitRunner(t, expected)
	var stdout bytes.Buffer
	if err := syncGitCheckout(context.Background(), &stdout, "", run); err != nil {
		t.Fatalf("syncGitCheckout() error = %v", err)
	}
	assertComplete()
	if got, want := stdout.String(), "Syncing origin/main\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestSyncGitCheckoutSkipsDetachedCheckoutWithoutOverride(t *testing.T) {
	t.Parallel()

	expected := []expectedGitCommand{
		{quiet: true, args: []string{"rev-parse", "--is-inside-work-tree"}, output: "true"},
		{quiet: true, args: []string{"status", "--porcelain"}},
		{quiet: true, args: []string{"rev-parse", "--abbrev-ref", "HEAD"}, output: "HEAD"},
	}
	run, assertComplete := newExpectedGitRunner(t, expected)
	var stdout bytes.Buffer
	if err := syncGitCheckout(context.Background(), &stdout, "", run); err != nil {
		t.Fatalf("syncGitCheckout() error = %v", err)
	}
	assertComplete()
	if got, want := stdout.String(), "Skipping git sync: checkout is detached\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestSyncGitCheckoutRejectsDirtyCheckoutBeforeFetch(t *testing.T) {
	t.Parallel()

	expected := []expectedGitCommand{
		{quiet: true, args: []string{"rev-parse", "--is-inside-work-tree"}, output: "true"},
		{quiet: true, args: []string{"status", "--porcelain"}, output: " M compose.yaml\n"},
	}
	run, assertComplete := newExpectedGitRunner(t, expected)
	err := syncGitCheckout(context.Background(), io.Discard, "main", run)
	if err == nil || !strings.Contains(err.Error(), "refusing Git sync: checkout has local changes") {
		t.Fatalf("syncGitCheckout() error = %v, want dirty-checkout refusal", err)
	}
	assertComplete()
}

func TestSyncGitCheckoutExplicitBranchRequiresGitCheckout(t *testing.T) {
	t.Parallel()

	notGit := errors.New("not a git repository")
	expected := []expectedGitCommand{{quiet: true, args: []string{"rev-parse", "--is-inside-work-tree"}, err: notGit}}
	run, assertComplete := newExpectedGitRunner(t, expected)
	err := syncGitCheckout(context.Background(), io.Discard, "main", run)
	if !errors.Is(err, notGit) || !strings.Contains(err.Error(), "requires a Git checkout") {
		t.Fatalf("syncGitCheckout() error = %v, want explicit-checkout refusal", err)
	}
	assertComplete()
}

func TestSyncGitCheckoutRejectsInvalidBranchBeforeFetch(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("invalid branch")
	expected := []expectedGitCommand{
		{quiet: true, args: []string{"rev-parse", "--is-inside-work-tree"}, output: "true"},
		{quiet: true, args: []string{"status", "--porcelain"}},
		{quiet: true, args: []string{"rev-parse", "--abbrev-ref", "HEAD"}, output: "main"},
		{quiet: true, args: []string{"check-ref-format", "--branch", "bad\nbranch"}, err: wantErr},
	}
	run, assertComplete := newExpectedGitRunner(t, expected)
	err := syncGitCheckout(context.Background(), io.Discard, "bad\nbranch", run)
	if !errors.Is(err, wantErr) || !strings.Contains(err.Error(), "validate Git branch") {
		t.Fatalf("syncGitCheckout() error = %v, want contextualized validation error", err)
	}
	assertComplete()
}

func TestSyncGitRefCheckoutUsesDedicatedRefAndDetachedCommit(t *testing.T) {
	t.Parallel()

	ref := "refs/pull/7/head;touch-not-executed"
	commit := "0123456789abcdef0123456789abcdef01234567"
	expected := []expectedGitCommand{
		{quiet: true, args: []string{"rev-parse", "--is-inside-work-tree"}, output: "true"},
		{quiet: true, args: []string{"status", "--porcelain"}},
		{quiet: true, args: []string{"check-ref-format", "refs/sitectl/input/" + ref}},
		{quiet: true, args: []string{"rev-parse", "--abbrev-ref", "HEAD"}, output: "main"},
		{quiet: true, args: []string{"config", "--get", "branch.main.remote"}, output: "origin"},
		{quiet: true, args: []string{"update-ref", "-d", gitDeployRef}},
		{args: []string{"fetch", "--no-tags", "--force", "--", "origin", ref + ":" + gitDeployRef}},
		{quiet: true, args: []string{"rev-parse", "--verify", gitDeployRef + "^{commit}"}, output: commit + "\n"},
		{args: []string{"checkout", "--detach", commit}},
	}
	run, assertComplete := newExpectedGitRunner(t, expected)
	if err := syncGitRefCheckout(context.Background(), io.Discard, ref, run); err != nil {
		t.Fatalf("syncGitRefCheckout() error = %v", err)
	}
	assertComplete()
}

func TestSyncGitRefCheckoutRejectsRefspecSeparator(t *testing.T) {
	t.Parallel()

	expected := []expectedGitCommand{
		{quiet: true, args: []string{"rev-parse", "--is-inside-work-tree"}, output: "true"},
		{quiet: true, args: []string{"status", "--porcelain"}},
	}
	run, assertComplete := newExpectedGitRunner(t, expected)
	err := syncGitRefCheckout(context.Background(), io.Discard, "refs/heads/main:refs/heads/owned", run)
	if err == nil || !strings.Contains(err.Error(), "must not contain a refspec separator") {
		t.Fatalf("syncGitRefCheckout() error = %v, want refspec separator refusal", err)
	}
	assertComplete()
}

func TestSyncGitRefCheckoutRequiresGitCheckout(t *testing.T) {
	t.Parallel()

	notGit := errors.New("not a git repository")
	expected := []expectedGitCommand{{quiet: true, args: []string{"rev-parse", "--is-inside-work-tree"}, err: notGit}}
	run, assertComplete := newExpectedGitRunner(t, expected)
	err := syncGitRefCheckout(context.Background(), io.Discard, "refs/heads/main", run)
	if !errors.Is(err, notGit) || !strings.Contains(err.Error(), "requires a Git checkout") {
		t.Fatalf("syncGitRefCheckout() error = %v, want explicit-checkout refusal", err)
	}
	assertComplete()
}

func TestSyncGitRefCheckoutFetchErrorHasContextAndStopsBeforeCheckout(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("fetch denied")
	expected := []expectedGitCommand{
		{quiet: true, args: []string{"rev-parse", "--is-inside-work-tree"}, output: "true"},
		{quiet: true, args: []string{"status", "--porcelain"}},
		{quiet: true, args: []string{"check-ref-format", "refs/sitectl/input/refs/pull/7/head"}},
		{quiet: true, args: []string{"rev-parse", "--abbrev-ref", "HEAD"}, output: "main"},
		{quiet: true, args: []string{"config", "--get", "branch.main.remote"}, output: "origin"},
		{quiet: true, args: []string{"update-ref", "-d", gitDeployRef}},
		{args: []string{"fetch", "--no-tags", "--force", "--", "origin", "refs/pull/7/head:" + gitDeployRef}, err: wantErr},
	}
	run, assertComplete := newExpectedGitRunner(t, expected)
	err := syncGitRefCheckout(context.Background(), io.Discard, "refs/pull/7/head", run)
	if !errors.Is(err, wantErr) || !strings.Contains(err.Error(), `fetch exact Git ref "refs/pull/7/head" from remote "origin"`) {
		t.Fatalf("syncGitRefCheckout() error = %v, want contextualized fetch error", err)
	}
	assertComplete()
}

func TestSyncGitRefCheckoutFetchesSequentialRefs(t *testing.T) {
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

	ctx := &Context{DockerHostType: ContextLocal, ProjectDir: checkout}
	if err := ctx.SyncGitRefCheckout(context.Background(), io.Discard, "refs/pull/7/head"); err != nil {
		t.Fatalf("SyncGitRefCheckout(pull request) error = %v", err)
	}
	if got := strings.TrimSpace(gitTestRun(t, checkout, "rev-parse", "HEAD")); got != pullCommit {
		t.Fatalf("pull-request checkout = %s, want %s", got, pullCommit)
	}
	if got := strings.TrimSpace(gitTestRun(t, checkout, "branch", "--show-current")); got != "" {
		t.Fatalf("exact ref checkout remained on branch %q", got)
	}

	if err := ctx.SyncGitRefCheckout(context.Background(), io.Discard, "main"); err != nil {
		t.Fatalf("SyncGitRefCheckout(main) error = %v", err)
	}
	if got := strings.TrimSpace(gitTestRun(t, checkout, "rev-parse", "HEAD")); got != mainCommit {
		t.Fatalf("main ref checkout = %s, want %s", got, mainCommit)
	}
}

type expectedGitCommand struct {
	quiet  bool
	args   []string
	output string
	err    error
}

func newExpectedGitRunner(t *testing.T, expected []expectedGitCommand) (gitCommandRunner, func()) {
	t.Helper()
	call := 0
	run := func(_ context.Context, quiet bool, args ...string) (string, error) {
		t.Helper()
		if call >= len(expected) {
			t.Fatalf("unexpected Git command %d: quiet=%t args=%q", call, quiet, args)
		}
		want := expected[call]
		call++
		if quiet != want.quiet || !slices.Equal(args, want.args) {
			t.Fatalf("Git command %d = quiet=%t args=%q, want quiet=%t args=%q", call, quiet, args, want.quiet, want.args)
		}
		return want.output, want.err
	}
	assertComplete := func() {
		t.Helper()
		if call != len(expected) {
			t.Fatalf("executed %d Git commands, want %d; next command is %q", call, len(expected), expected[call].args)
		}
	}
	return run, assertComplete
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

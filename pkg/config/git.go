package config

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

const gitDeployRef = "refs/sitectl/deploy"

type gitCommandRunner func(context.Context, bool, ...string) (string, error)

// SyncGitCheckout fast-forwards the checkout from its configured upstream
// branch. An explicit branch is fetched from the checkout's selected remote
// before it is checked out. Non-Git directories and branches without an
// upstream are skipped only when no explicit branch was requested.
func (c *Context) SyncGitCheckout(runCtx context.Context, stdout io.Writer, branchOverride string) error {
	if c == nil {
		return fmt.Errorf("context is nil")
	}
	return syncGitCheckout(runCtx, stdout, branchOverride, c.runGitCommandContext)
}

// SyncGitRefCheckout fetches an exact remote ref (including refs/pull/* or an
// advertised commit) into a dedicated local ref, verifies that it resolves to
// a commit, and checks it out detached. It deliberately does not rewrite a
// configured branch or its upstream.
func (c *Context) SyncGitRefCheckout(runCtx context.Context, stdout io.Writer, ref string) error {
	if c == nil {
		return fmt.Errorf("context is nil")
	}
	return syncGitRefCheckout(runCtx, stdout, ref, c.runGitCommandContext)
}

func (c *Context) runGitCommandContext(runCtx context.Context, quiet bool, args ...string) (string, error) {
	command := exec.Command("git", args...) // #nosec G204 -- Git is fixed and every dynamic value remains a distinct argument.
	command.Dir = c.ProjectDir
	if quiet {
		return c.RunQuietCommandContext(runCtx, command)
	}
	return c.RunCommandContext(runCtx, command)
}

func syncGitCheckout(runCtx context.Context, stdout io.Writer, branchOverride string, run gitCommandRunner) error {
	if runCtx == nil {
		runCtx = context.Background()
	}
	if err := runCtx.Err(); err != nil {
		return err
	}
	if run == nil {
		return fmt.Errorf("git command runner is nil")
	}

	branch := strings.TrimSpace(branchOverride)
	inside, err := run(runCtx, true, "rev-parse", "--is-inside-work-tree")
	if err != nil {
		if ctxErr := runCtx.Err(); ctxErr != nil {
			return ctxErr
		}
		if branch != "" {
			return fmt.Errorf("explicit Git branch sync requires a Git checkout: %w", err)
		}
		writeGitSyncMessage(stdout, "Skipping git sync: project is not a git checkout")
		return nil
	}
	if strings.TrimSpace(inside) != "true" {
		if branch != "" {
			return fmt.Errorf("explicit Git branch sync requires a Git checkout")
		}
		writeGitSyncMessage(stdout, "Skipping git sync: project is not a git checkout")
		return nil
	}
	if err := requireCleanGitCheckout(runCtx, run, "sync"); err != nil {
		return err
	}

	currentBranch, err := runGitQuery(runCtx, run, "resolve current Git branch", "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return err
	}
	if branch != "" {
		if _, err := run(runCtx, true, "check-ref-format", "--branch", branch); err != nil {
			return fmt.Errorf("validate Git branch %q: %w", branch, err)
		}

		_, localBranchErr := run(runCtx, true, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
		localBranchExists := localBranchErr == nil
		remote, err := resolveGitSyncRemote(runCtx, run, branch, "Explicit branch sync")
		if err != nil {
			return err
		}
		remoteRef := "refs/remotes/" + remote + "/" + branch
		refspec := "refs/heads/" + branch + ":" + remoteRef
		writeGitSyncMessage(stdout, fmt.Sprintf("Fetching branch %s from %s", branch, remote))
		if _, err := run(runCtx, false, "fetch", "--prune", "--no-tags", "--", remote, refspec); err != nil {
			return fmt.Errorf("fetch Git branch %q from remote %q: %w", branch, remote, err)
		}
		resolved, err := runGitQuery(runCtx, run, "resolve fetched Git branch to a commit", "rev-parse", "--verify", remoteRef+"^{commit}")
		if err != nil {
			return err
		}
		if !isGitObjectID(resolved) {
			return fmt.Errorf("resolve fetched Git branch to a commit: Git returned an invalid object ID %q", resolved)
		}

		if localBranchExists {
			// Prove the final branch reset is a fast-forward before touching the
			// worktree. checkout -B then performs the only worktree mutation.
			if _, err := run(runCtx, true, "merge-base", "--is-ancestor", "refs/heads/"+branch, remoteRef); err != nil {
				return fmt.Errorf("refusing non-fast-forward Git branch %q from %q: %w", branch, remoteRef, err)
			}
			if _, err := run(runCtx, false, "checkout", "-B", branch, remoteRef, "--"); err != nil {
				return fmt.Errorf("check out fast-forwarded Git branch %q from %q: %w", branch, remoteRef, err)
			}
		} else {
			if _, err := run(runCtx, false, "checkout", "--track", "-b", branch, remoteRef); err != nil {
				return fmt.Errorf("create local Git branch %q tracking %q: %w", branch, remoteRef, err)
			}
		}
		return nil
	}

	if currentBranch == "HEAD" {
		writeGitSyncMessage(stdout, "Skipping git sync: checkout is detached")
		return nil
	}
	upstream, err := run(runCtx, true, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}")
	if err != nil {
		if ctxErr := runCtx.Err(); ctxErr != nil {
			return ctxErr
		}
		writeGitSyncMessage(stdout, fmt.Sprintf("Skipping git sync: branch %q has no upstream", currentBranch))
		return nil
	}
	if strings.TrimSpace(upstream) == "" {
		writeGitSyncMessage(stdout, fmt.Sprintf("Skipping git sync: branch %q has no upstream", currentBranch))
		return nil
	}
	upstream = strings.TrimSpace(upstream)
	writeGitSyncMessage(stdout, "Syncing "+upstream)
	if _, err := run(runCtx, false, "pull", "--ff-only"); err != nil {
		return fmt.Errorf("fast-forward Git branch %q from upstream %q: %w", currentBranch, upstream, err)
	}
	return nil
}

func syncGitRefCheckout(runCtx context.Context, stdout io.Writer, ref string, run gitCommandRunner) error {
	if runCtx == nil {
		runCtx = context.Background()
	}
	if err := runCtx.Err(); err != nil {
		return err
	}
	if run == nil {
		return fmt.Errorf("git command runner is nil")
	}

	inside, err := run(runCtx, true, "rev-parse", "--is-inside-work-tree")
	if err != nil {
		if ctxErr := runCtx.Err(); ctxErr != nil {
			return ctxErr
		}
		return fmt.Errorf("explicit Git ref checkout requires a Git checkout: %w", err)
	}
	if strings.TrimSpace(inside) != "true" {
		return fmt.Errorf("explicit Git ref checkout requires a Git checkout")
	}
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return fmt.Errorf("an explicit Git ref is required")
	}
	if err := requireCleanGitCheckout(runCtx, run, "ref checkout"); err != nil {
		return err
	}
	if err := validateExactGitFetchSource(runCtx, run, ref); err != nil {
		return err
	}

	currentBranch, err := runGitQuery(runCtx, run, "resolve current Git branch", "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return err
	}
	remote, err := resolveGitSyncRemote(runCtx, run, currentBranch, "Explicit ref sync")
	if err != nil {
		return err
	}

	if _, err := run(runCtx, true, "update-ref", "-d", gitDeployRef); err != nil {
		return fmt.Errorf("reset local Git deploy ref %q: %w", gitDeployRef, err)
	}
	writeGitSyncMessage(stdout, fmt.Sprintf("Fetching ref %s from %s", ref, remote))
	if _, err := run(runCtx, false, "fetch", "--no-tags", "--force", "--", remote, ref+":"+gitDeployRef); err != nil {
		return fmt.Errorf("fetch exact Git ref %q from remote %q: %w", ref, remote, err)
	}
	resolved, err := runGitQuery(runCtx, run, "resolve fetched Git ref to a commit", "rev-parse", "--verify", gitDeployRef+"^{commit}")
	if err != nil {
		return err
	}
	if !isGitObjectID(resolved) {
		return fmt.Errorf("resolve fetched Git ref to a commit: Git returned an invalid object ID %q", resolved)
	}
	if _, err := run(runCtx, false, "checkout", "--detach", resolved); err != nil {
		return fmt.Errorf("check out exact Git ref %q at commit %q: %w", ref, resolved, err)
	}
	return nil
}

func validateExactGitFetchSource(runCtx context.Context, run gitCommandRunner, ref string) error {
	if strings.ContainsRune(ref, ':') {
		return fmt.Errorf("Git ref %q must not contain a refspec separator", ref)
	}
	if strings.HasPrefix(ref, "+") || strings.HasPrefix(ref, "^") {
		return fmt.Errorf("Git ref %q must not contain a refspec operator", ref)
	}
	// Prefixing the candidate makes both one-level names and full refs safe
	// positional input to check-ref-format. It also rejects control characters,
	// revision expressions, and wildcard refspecs before the deploy ref is reset.
	if _, err := run(runCtx, true, "check-ref-format", "refs/sitectl/input/"+ref); err != nil {
		return fmt.Errorf("validate exact Git ref %q: %w", ref, err)
	}
	return nil
}

func isGitObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func requireCleanGitCheckout(runCtx context.Context, run gitCommandRunner, operation string) error {
	status, err := runGitQuery(runCtx, run, "inspect Git checkout changes", "status", "--porcelain")
	if err != nil {
		return err
	}
	if status != "" {
		return fmt.Errorf("refusing Git %s: checkout has local changes", operation)
	}
	return nil
}

func resolveGitSyncRemote(runCtx context.Context, run gitCommandRunner, currentBranch, operation string) (string, error) {
	remote, _ := run(runCtx, true, "config", "--get", "branch."+currentBranch+".remote")
	remote = strings.TrimSpace(remote)
	if remote != "" && remote != "." {
		return remote, nil
	}
	if _, err := run(runCtx, true, "remote", "get-url", "origin"); err == nil {
		return "origin", nil
	}
	remotes, err := runGitQuery(runCtx, run, "list Git remotes", "remote")
	if err != nil {
		return "", err
	}
	remoteNames := strings.Fields(remotes)
	if len(remoteNames) != 1 {
		return "", fmt.Errorf("%s requires an origin or exactly one remote", operation)
	}
	return remoteNames[0], nil
}

func runGitQuery(runCtx context.Context, run gitCommandRunner, operation string, args ...string) (string, error) {
	output, err := run(runCtx, true, args...)
	if err != nil {
		return "", fmt.Errorf("%s: %w", operation, err)
	}
	return strings.TrimSpace(output), nil
}

func writeGitSyncMessage(stdout io.Writer, message string) {
	if stdout != nil {
		_, _ = fmt.Fprintln(stdout, message)
	}
}

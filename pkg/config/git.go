package config

import (
	"fmt"
	"strings"

	"github.com/kballard/go-shellquote"
)

// GitSyncShellCommand returns a shell command that fast-forwards the checkout
// from the checkout's configured upstream branch. An explicit branch is
// fetched from the checkout's selected remote before it is checked out. It
// skips non-git directories and checkouts without an upstream branch only when
// no explicit branch was requested.
func (c Context) GitSyncShellCommand(branchOverride string) string {
	return GitSyncShellCommand(branchOverride)
}

func GitSyncShellCommand(branch string) string {
	branch = strings.TrimSpace(branch)
	return fmt.Sprintf(
		"set -euo pipefail; git_branch=%s; "+
			"if ! git rev-parse --is-inside-work-tree >/dev/null 2>&1; then echo \"Skipping git sync: project is not a git checkout\"; exit 0; fi; "+
			"if [ -n \"$(git status --porcelain)\" ]; then echo \"Refusing git sync: checkout has local changes\" >&2; exit 1; fi; "+
			"current_branch=$(git rev-parse --abbrev-ref HEAD); "+
			"if [ -n \"$git_branch\" ]; then "+
			"git check-ref-format --branch \"$git_branch\" >/dev/null; "+
			"remote=$(git config --get \"branch.${current_branch}.remote\" 2>/dev/null || true); "+
			"if [ -z \"$remote\" ] || [ \"$remote\" = . ]; then if git remote get-url origin >/dev/null 2>&1; then remote=origin; else remote_count=$(git remote | awk 'NF { count++ } END { print count + 0 }'); [ \"$remote_count\" -eq 1 ] || { echo \"Explicit branch sync requires an origin or exactly one remote\" >&2; exit 1; }; remote=$(git remote); fi; fi; "+
			"echo \"Fetching branch ${git_branch} from ${remote}\"; "+
			"git fetch --prune --no-tags \"$remote\" \"refs/heads/${git_branch}:refs/remotes/${remote}/${git_branch}\"; "+
			"if git show-ref --verify --quiet \"refs/heads/${git_branch}\"; then git checkout \"$git_branch\"; else git checkout -b \"$git_branch\" --track \"${remote}/${git_branch}\"; fi; "+
			"git merge --ff-only \"${remote}/${git_branch}\"; exit 0; fi; "+
			"if [ \"$current_branch\" = HEAD ]; then echo \"Skipping git sync: checkout is detached\"; exit 0; fi; "+
			"upstream=$(git rev-parse --abbrev-ref --symbolic-full-name '@{u}' 2>/dev/null || true); "+
			"if [ -z \"$upstream\" ]; then echo \"Skipping git sync: branch '$current_branch' has no upstream\"; exit 0; fi; "+
			"echo \"Syncing ${upstream}\"; "+
			"git pull --ff-only",
		shellquote.Join(branch),
	)
}

// GitSyncRefShellCommand returns a shell command that fetches an exact remote
// ref (including refs/pull/* or an advertised commit) into a dedicated local
// ref, verifies that it resolves to a commit, and checks it out detached. This
// deliberately does not rewrite a configured branch or its upstream.
func (c Context) GitSyncRefShellCommand(ref string) string {
	return GitSyncRefShellCommand(ref)
}

func GitSyncRefShellCommand(ref string) string {
	ref = strings.TrimSpace(ref)
	return fmt.Sprintf(
		"set -euo pipefail; git_ref=%s; "+
			"if ! git rev-parse --is-inside-work-tree >/dev/null 2>&1; then echo \"Skipping git sync: project is not a git checkout\"; exit 0; fi; "+
			"if [ -z \"$git_ref\" ]; then echo \"An explicit git ref is required\" >&2; exit 2; fi; "+
			"if [ -n \"$(git status --porcelain)\" ]; then echo \"Refusing git ref checkout: checkout has local changes\" >&2; exit 1; fi; "+
			"current_branch=$(git rev-parse --abbrev-ref HEAD); "+
			"remote=$(git config --get \"branch.${current_branch}.remote\" 2>/dev/null || true); "+
			"if [ -z \"$remote\" ] || [ \"$remote\" = . ]; then if git remote get-url origin >/dev/null 2>&1; then remote=origin; else remote_count=$(git remote | awk 'NF { count++ } END { print count + 0 }'); [ \"$remote_count\" -eq 1 ] || { echo \"Explicit ref sync requires an origin or exactly one remote\" >&2; exit 1; }; remote=$(git remote); fi; fi; "+
			"git update-ref -d refs/sitectl/deploy; "+
			"echo \"Fetching ref ${git_ref} from ${remote}\"; "+
			"git fetch --no-tags --force \"$remote\" \"${git_ref}:refs/sitectl/deploy\"; "+
			"resolved=$(git rev-parse --verify 'refs/sitectl/deploy^{commit}'); "+
			"git checkout --detach \"$resolved\"",
		shellquote.Join(ref),
	)
}

package config

import (
	"fmt"
	"strings"

	"github.com/kballard/go-shellquote"
)

// GitSyncShellCommand returns a shell command that fast-forwards the checkout
// from the checkout's configured upstream branch. It skips non-git directories
// and git checkouts without an upstream branch.
func (c Context) GitSyncShellCommand(branchOverride string) string {
	return GitSyncShellCommand(branchOverride)
}

func GitSyncShellCommand(branch string) string {
	branch = strings.TrimSpace(branch)
	return fmt.Sprintf(
		"git_branch=%s; "+
			"if ! git rev-parse --is-inside-work-tree >/dev/null 2>&1; then echo \"Skipping git sync: project is not a git checkout\"; exit 0; fi; "+
			"if [ -n \"$git_branch\" ]; then git checkout \"$git_branch\"; fi; "+
			"current_branch=$(git rev-parse --abbrev-ref HEAD); "+
			"if [ \"$current_branch\" = HEAD ]; then echo \"Skipping git sync: checkout is detached\"; exit 0; fi; "+
			"upstream=$(git rev-parse --abbrev-ref --symbolic-full-name '@{u}' 2>/dev/null || true); "+
			"if [ -z \"$upstream\" ]; then echo \"Skipping git sync: branch '$current_branch' has no upstream\"; exit 0; fi; "+
			"echo \"Syncing ${upstream}\"; "+
			"git pull --ff-only",
		shellquote.Join(branch),
	)
}

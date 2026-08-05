package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/libops/sitectl/pkg/config"
	"github.com/spf13/cobra"
)

var (
	deleteContextInput          = config.GetInput
	deleteContextRunComposeDown = runComposeDownVolumes
	deleteContextRemoveProject  = os.RemoveAll
)

func runDeleteContextCommand(cmd *cobra.Command, name string, deleteProject bool) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, index, ok := contextForDeletion(cfg, name)
	if !ok {
		return fmt.Errorf("context %q not found", name)
	}

	if !deleteProject {
		if strings.EqualFold(cfg.CurrentContext, ctx.Name) {
			return fmt.Errorf("cannot delete the current context; switch to another context first")
		}
		updated, _ := configWithoutContext(cfg, index)
		if err := config.Save(updated); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Deleted context: %s\n", ctx.Name)
		return nil
	}

	updated, replacement := configWithoutContext(cfg, index)
	deletingCurrent := strings.EqualFold(cfg.CurrentContext, ctx.Name)
	if err := confirmContextRecordDeletion(ctx, deletingCurrent, replacement); err != nil {
		return err
	}

	projectDir, projectExists, err := localProjectDeletionTarget(cfg, ctx)
	if err != nil {
		return err
	}
	if projectExists {
		if err := confirmLocalProjectDeletion(ctx, projectDir); err != nil {
			return err
		}
	}

	// Save the context removal before invoking destructive cleanup, as promised
	// by the confirmation. Restore it when a synchronous cleanup step fails so
	// the operator keeps a usable target for diagnosis or retry.
	if err := config.Save(updated); err != nil {
		return err
	}
	restore := func(cleanupErr error) error {
		if restoreErr := config.Save(cfg); restoreErr != nil {
			return fmt.Errorf("%w; also failed to restore context %q: %v", cleanupErr, ctx.Name, restoreErr)
		}
		return cleanupErr
	}

	if projectExists {
		ctx.ProjectDir = projectDir
		if err := deleteContextRunComposeDown(cmd, &ctx); err != nil {
			return restore(fmt.Errorf("clean Compose project before deleting context: %w", err))
		}
		revalidatedDir, stillExists, err := localProjectDeletionTarget(cfg, ctx)
		if err != nil {
			return restore(fmt.Errorf("revalidate project directory after Compose teardown: %w", err))
		}
		if stillExists && revalidatedDir != projectDir {
			return restore(fmt.Errorf("project directory resolved to %s after confirmation; refusing to delete expected path %s", revalidatedDir, projectDir))
		}
		if stillExists {
			if err := deleteContextRemoveProject(projectDir); err != nil {
				return restore(fmt.Errorf("delete project directory %s: %w", projectDir, err))
			}
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Deleted local project directory: %s\n", projectDir)
	} else if ctx.DockerHostType == config.ContextRemote {
		fmt.Fprintln(cmd.OutOrStdout(), "Remote containers and project files were left unchanged.")
	} else {
		fmt.Fprintln(cmd.OutOrStdout(), "No local project directory exists; skipped Compose teardown and file deletion.")
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Deleted context: %s\n", ctx.Name)
	if deletingCurrent && strings.TrimSpace(replacement) != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Current context is now: %s\n", replacement)
	} else if deletingCurrent {
		fmt.Fprintln(cmd.OutOrStdout(), "No current context remains.")
	}
	return nil
}

func contextForDeletion(cfg *config.Config, name string) (config.Context, int, bool) {
	if cfg == nil {
		return config.Context{}, -1, false
	}
	for index, ctx := range cfg.Contexts {
		if strings.EqualFold(ctx.Name, strings.TrimSpace(name)) {
			return ctx, index, true
		}
	}
	return config.Context{}, -1, false
}

func configWithoutContext(cfg *config.Config, index int) (*config.Config, string) {
	updated := &config.Config{CurrentContext: cfg.CurrentContext}
	updated.Contexts = make([]config.Context, 0, max(0, len(cfg.Contexts)-1))
	updated.Contexts = append(updated.Contexts, cfg.Contexts[:index]...)
	updated.Contexts = append(updated.Contexts, cfg.Contexts[index+1:]...)

	replacement := updated.CurrentContext
	if strings.EqualFold(updated.CurrentContext, cfg.Contexts[index].Name) {
		replacement = ""
		if len(updated.Contexts) > 0 {
			replacement = updated.Contexts[0].Name
		}
		updated.CurrentContext = replacement
	}
	return updated, replacement
}

func confirmContextRecordDeletion(ctx config.Context, deletingCurrent bool, replacement string) error {
	const token = "delete"
	defaultChange := "The current default context will not change."
	if deletingCurrent && strings.TrimSpace(replacement) == "" {
		defaultChange = "No default context will remain if this is the current context."
	} else if deletingCurrent {
		defaultChange = fmt.Sprintf("The default context will switch to %q.", replacement)
	}
	answer, err := deleteContextInput(
		fmt.Sprintf("Delete saved context %q from this machine?", ctx.Name),
		"This confirmation removes its local sitectl connection record.",
		defaultChange,
		fmt.Sprintf("Type %q to continue: ", token),
	)
	if err != nil {
		return err
	}
	if strings.TrimSpace(answer) != token {
		return fmt.Errorf("context deletion cancelled")
	}
	return nil
}

func confirmLocalProjectDeletion(ctx config.Context, projectDir string) error {
	token := "wipe " + ctx.Name
	answer, err := deleteContextInput(
		fmt.Sprintf("Permanently wipe the local project for context %q?", ctx.Name),
		fmt.Sprintf("Project directory: %s", projectDir),
		"sitectl will run 'docker compose down -v' in this directory, permanently deleting Compose volumes.",
		"After Compose stops, sitectl will permanently delete the complete project directory.",
		fmt.Sprintf("Type %q to continue: ", token),
	)
	if err != nil {
		return err
	}
	if strings.TrimSpace(answer) != token {
		return fmt.Errorf("local project deletion cancelled")
	}
	return nil
}

func localProjectDeletionTarget(cfg *config.Config, ctx config.Context) (string, bool, error) {
	switch ctx.DockerHostType {
	case config.ContextRemote:
		return "", false, nil
	case config.ContextLocal:
	default:
		return "", false, fmt.Errorf("context %q has unknown target type %q; refusing project deletion", ctx.Name, ctx.DockerHostType)
	}

	projectDir := strings.TrimSpace(ctx.ProjectDir)
	if projectDir == "" {
		return "", false, nil
	}
	absProjectDir, err := filepath.Abs(projectDir)
	if err != nil {
		return "", false, fmt.Errorf("resolve project directory %q: %w", projectDir, err)
	}
	absProjectDir = filepath.Clean(absProjectDir)
	info, err := os.Lstat(absProjectDir)
	if os.IsNotExist(err) {
		return absProjectDir, false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("inspect project directory %s: %w", absProjectDir, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", false, fmt.Errorf("refusing to delete symlinked project directory %s", absProjectDir)
	}
	if !info.IsDir() {
		return "", false, fmt.Errorf("project path %s is not a directory", absProjectDir)
	}

	resolvedProjectDir, err := filepath.EvalSymlinks(absProjectDir)
	if err != nil {
		return "", false, fmt.Errorf("resolve project directory symlinks for %s: %w", absProjectDir, err)
	}
	resolvedProjectDir = filepath.Clean(resolvedProjectDir)
	if err := validateProjectDeletionPath(resolvedProjectDir); err != nil {
		return "", false, err
	}
	if sharedWith := sharedLocalProjectContext(cfg, ctx.Name, resolvedProjectDir); sharedWith != "" {
		return "", false, fmt.Errorf("refusing to delete project directory %s because context %q also uses it", resolvedProjectDir, sharedWith)
	}
	return resolvedProjectDir, true, nil
}

func validateProjectDeletionPath(projectDir string) error {
	volumeRoot := filepath.Clean(filepath.VolumeName(projectDir) + string(filepath.Separator))
	if projectDir == volumeRoot {
		return fmt.Errorf("refusing to delete filesystem root %s", projectDir)
	}
	if pathDepth(projectDir) < 2 {
		return fmt.Errorf("refusing to delete broad project directory %s", projectDir)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve home directory before project deletion: %w", err)
	}
	resolvedHome, err := filepath.EvalSymlinks(home)
	if err != nil {
		resolvedHome = filepath.Clean(home)
	}
	if pathContains(projectDir, resolvedHome) {
		return fmt.Errorf("refusing to delete %s because it contains the home directory %s", projectDir, resolvedHome)
	}
	return nil
}

func sharedLocalProjectContext(cfg *config.Config, deletingName, resolvedProjectDir string) string {
	if cfg == nil {
		return ""
	}
	for _, other := range cfg.Contexts {
		if strings.EqualFold(other.Name, deletingName) || other.DockerHostType != config.ContextLocal {
			continue
		}
		otherDir, err := filepath.Abs(strings.TrimSpace(other.ProjectDir))
		if err != nil || strings.TrimSpace(other.ProjectDir) == "" {
			continue
		}
		if resolved, resolveErr := filepath.EvalSymlinks(otherDir); resolveErr == nil {
			otherDir = resolved
		}
		if filepath.Clean(otherDir) == resolvedProjectDir {
			return other.Name
		}
	}
	return ""
}

func pathContains(parent, child string) bool {
	relative, err := filepath.Rel(filepath.Clean(parent), filepath.Clean(child))
	if err != nil {
		return false
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}

func pathDepth(value string) int {
	trimmed := strings.Trim(filepath.Clean(value), string(filepath.Separator))
	if trimmed == "" {
		return 0
	}
	return len(strings.Split(trimmed, string(filepath.Separator)))
}

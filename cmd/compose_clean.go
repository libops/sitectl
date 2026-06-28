package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/libops/sitectl/pkg/config"
	"github.com/libops/sitectl/pkg/plugin"
	"github.com/spf13/cobra"
)

var (
	composeCleanInput = config.GetInput
)

var composeCleanCmd = &cobra.Command{
	Use:   "clean",
	Short: "Destroy local Compose containers, volumes, and plugin init state",
	Long: `Destroy local Compose containers, volumes, and plugin init state.

This command runs 'docker compose down -v', removes init artifacts declared by
the active plugin, and clears the local compose reconcile cache entry. It is
destructive: database volumes, uploaded files stored in named volumes, generated
secrets, certificates, and env files declared by the plugin can be lost.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		yes, err := cmd.Flags().GetBool("yes")
		if err != nil {
			return err
		}
		return runComposeCleanCommand(cmd, yes)
	},
}

func init() {
	composeCleanCmd.Flags().Bool("yes", false, "Skip the destructive confirmation prompt.")
	composeCmd.AddCommand(composeCleanCmd)
}

func runComposeCleanCommand(cmd *cobra.Command, yes bool) error {
	ctx, spec, err := composeCleanContext(cmd)
	if err != nil {
		return err
	}
	if err := confirmComposeClean(ctx, yes); err != nil {
		return err
	}
	if err := runComposeDownVolumes(cmd, ctx); err != nil {
		return err
	}
	removed, err := composeReconcileReset(ctx, spec)
	if err != nil {
		return err
	}
	for _, name := range removed {
		fmt.Fprintf(cmd.OutOrStdout(), "Removed %s\n", name)
	}
	if err := composeReconcileClear(ctx, spec); err != nil {
		return err
	}
	fmt.Fprintln(cmd.OutOrStdout(), "Compose project cleaned")
	return nil
}

func composeCleanContext(cmd *cobra.Command) (*config.Context, plugin.CreateSpec, error) {
	ctx, err := resolveCurrentContext(cmd)
	if err != nil {
		return nil, plugin.CreateSpec{}, err
	}
	if ctx.DockerHostType != config.ContextLocal {
		return nil, plugin.CreateSpec{}, fmt.Errorf("compose clean currently requires a local context")
	}
	if strings.TrimSpace(ctx.Plugin) == "" || strings.TrimSpace(ctx.Plugin) == "core" {
		return nil, plugin.CreateSpec{}, fmt.Errorf("context %q is not managed by a plugin", ctx.Name)
	}
	if strings.TrimSpace(ctx.ProjectDir) == "" {
		return nil, plugin.CreateSpec{}, fmt.Errorf("context %q does not define a project directory", ctx.Name)
	}
	spec, ok, err := composeReconcileSpec(strings.TrimSpace(ctx.Plugin))
	if err != nil {
		return nil, plugin.CreateSpec{}, err
	}
	if !ok {
		return nil, plugin.CreateSpec{}, fmt.Errorf("plugin %q does not define a create lifecycle", ctx.Plugin)
	}
	return ctx, spec, nil
}

func confirmComposeClean(ctx *config.Context, yes bool) error {
	if yes {
		return nil
	}
	contextName := ""
	projectDir := ""
	if ctx != nil {
		contextName = strings.TrimSpace(ctx.Name)
		projectDir = strings.TrimSpace(ctx.ProjectDir)
	}
	if contextName == "" {
		contextName = "this context"
	}
	token := "delete " + contextName
	input, err := composeCleanInput(
		fmt.Sprintf("This will permanently delete local Docker Compose volumes and plugin init files for %q.", contextName),
		fmt.Sprintf("Project directory: %s", projectDir),
		"Database contents, uploaded files stored in named volumes, generated secrets, certificates, and declared env files can be lost.",
		fmt.Sprintf("Type %q to continue: ", token),
	)
	if err != nil {
		return err
	}
	if strings.TrimSpace(input) != token {
		return fmt.Errorf("compose clean cancelled")
	}
	return nil
}

func runComposeDownVolumes(cmd *cobra.Command, ctx *config.Context) error {
	commandText := ctx.DockerComposeShellCommand("docker compose down -v")
	fmt.Fprintf(cmd.OutOrStdout(), "Running %s\n", commandText)
	command := exec.CommandContext(cmd.Context(), "bash", "-lc", commandText) // #nosec G204 -- fixed docker compose command rewritten from context-owned metadata.
	command.Dir = ctx.ProjectDir
	command.Env = os.Environ()
	command.Stdin = cmd.InOrStdin()
	command.Stdout = cmd.OutOrStdout()
	command.Stderr = cmd.ErrOrStderr()
	if err := command.Run(); err != nil {
		return fmt.Errorf("run %s: %w", commandText, err)
	}
	return nil
}

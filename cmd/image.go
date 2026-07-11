package cmd

import (
	"fmt"
	"strings"

	"github.com/libops/sitectl/pkg/config"
	"github.com/libops/sitectl/pkg/plugin"
	"github.com/spf13/cobra"
)

var imageCmd = &cobra.Command{
	Use:   "image",
	Short: "Manage Compose image overrides for a site",
}

var imageSetCmd = &cobra.Command{
	Use:   "set",
	Short: "Write Compose image or build-arg overrides",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, err := resolveCurrentContext(cmd)
		if err != nil {
			return err
		}
		if strings.TrimSpace(ctx.ProjectDir) == "" {
			return fmt.Errorf("context %q does not define a project directory", ctx.Name)
		}
		warnRemoteProjectMutation(cmd, ctx)
		imageTags, err := cmd.Flags().GetStringArray("tag")
		if err != nil {
			return err
		}
		images, err := cmd.Flags().GetStringArray("image")
		if err != nil {
			return err
		}
		buildArgs, err := cmd.Flags().GetStringArray("build-arg")
		if err != nil {
			return err
		}
		overrides, err := plugin.ResolveComposeImageOverrides(ctx.Plugin, imageTags, images, buildArgs)
		if err != nil {
			return err
		}
		if overrides.Empty() {
			return fmt.Errorf("no image overrides requested")
		}
		if err := plugin.ApplyComposeImageOverridesContext(ctx, overrides); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Wrote %s\n", plugin.ComposeImageOverrideFile)
		return nil
	},
}

var imageClearCmd = &cobra.Command{
	Use:   "clear [SERVICE...]",
	Short: "Remove Compose image and build-arg overrides",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, err := resolveCurrentContext(cmd)
		if err != nil {
			return err
		}
		if strings.TrimSpace(ctx.ProjectDir) == "" {
			return fmt.Errorf("context %q does not define a project directory", ctx.Name)
		}
		warnRemoteProjectMutation(cmd, ctx)
		if err := plugin.ClearComposeImageOverridesContext(ctx, args); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Updated %s\n", plugin.ComposeImageOverrideFile)
		return nil
	},
}

func init() {
	imageSetCmd.Flags().StringArray("tag", []string{}, "Set a LibOps image tag for a known Compose service as SERVICE=TAG; may be passed more than once.")
	imageSetCmd.Flags().StringArray("image", []string{}, "Override a non-buildable Compose service image as SERVICE=IMAGE; may be passed more than once.")
	imageSetCmd.Flags().StringArray("build-arg", []string{}, "Override a Compose service build arg as SERVICE.ARG=VALUE; may be passed more than once.")
	imageCmd.AddCommand(imageSetCmd)
	imageCmd.AddCommand(imageClearCmd)
	imageCmd.GroupID = "workflow"
	RootCmd.AddCommand(imageCmd)
}

func warnRemoteProjectMutation(cmd *cobra.Command, ctx *config.Context) {
	if cmd == nil || ctx == nil || ctx.DockerHostType != config.ContextRemote {
		return
	}
	fmt.Fprintln(cmd.ErrOrStderr(), "Warning: modifying remote project files directly; commit and review these changes through version control before promoting them.")
}

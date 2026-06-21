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
		if ctx.DockerHostType != config.ContextLocal {
			return fmt.Errorf("image override updates currently require a local context")
		}
		if strings.TrimSpace(ctx.ProjectDir) == "" {
			return fmt.Errorf("context %q does not define a project directory", ctx.Name)
		}
		buildkitTag, err := cmd.Flags().GetString("buildkit-tag")
		if err != nil {
			return err
		}
		buildkitRepository, err := cmd.Flags().GetString("buildkit-repository")
		if err != nil {
			return err
		}
		imageRefs, err := cmd.Flags().GetStringArray("image-ref")
		if err != nil {
			return err
		}
		buildArgs, err := cmd.Flags().GetStringArray("build-arg")
		if err != nil {
			return err
		}
		overrides, err := plugin.ResolveComposeImageOverrides(ctx.Plugin, buildkitRepository, buildkitTag, imageRefs, buildArgs)
		if err != nil {
			return err
		}
		if overrides.Empty() {
			return fmt.Errorf("no image overrides requested")
		}
		if err := plugin.ApplyComposeImageOverrides(ctx.ProjectDir, overrides); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Wrote %s\n", plugin.ComposeImageOverrideFile)
		return nil
	},
}

func init() {
	imageSetCmd.Flags().String("buildkit-tag", "", "Buildkit runtime tag to use as the template base image, such as nginx-1.30.2-php84.")
	imageSetCmd.Flags().String("buildkit-repository", "libops", "Container repository for --buildkit-tag image refs.")
	imageSetCmd.Flags().StringArray("image-ref", []string{}, "Override a Compose service image as SERVICE=IMAGE; may be passed more than once.")
	imageSetCmd.Flags().StringArray("build-arg", []string{}, "Override a Compose service build arg as SERVICE.ARG=VALUE; may be passed more than once.")
	imageCmd.AddCommand(imageSetCmd)
	imageCmd.GroupID = "workflow"
	RootCmd.AddCommand(imageCmd)
}

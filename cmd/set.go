package cmd

import (
	"fmt"
	"strings"

	"github.com/libops/sitectl/pkg/config"
	"github.com/libops/sitectl/pkg/helpers"
	"github.com/libops/sitectl/pkg/plugin"
	"github.com/spf13/cobra"
)

var setCmd = &cobra.Command{
	Use:   "set <component> [disposition] [flags]",
	Short: "Enable, disable, or reconfigure a component",
	Long: `Set the state or disposition of a named component for the active context.

The component name may be prefixed with the plugin namespace:

  sitectl set isle/fcrepo off
  sitectl set blazegraph disabled

This command dispatches to the plugin associated with the active context.
All flags and arguments are forwarded to the plugin's set handler.

Examples:
  sitectl set fcrepo off
  sitectl set isle/fcrepo disabled
  sitectl set isle-tls on --tls-mode letsencrypt`,
	DisableFlagParsing: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		filteredArgs, contextName, err := helpers.GetContextFromArgs(cmd, args)
		if err != nil {
			return err
		}
		if len(filteredArgs) == 0 {
			return fmt.Errorf("component name is required")
		}

		ctx, err := config.GetContext(contextName)
		if err != nil {
			return err
		}

		pluginName := strings.TrimSpace(ctx.Plugin)
		componentArg := filteredArgs[0]
		// Allow plugin/component namespacing: resolve the owning plugin.
		if pluginPart, _, ok := splitNamespacedComponent(componentArg); ok {
			pluginName = pluginPart
		}
		if pluginName == "" || pluginName == "core" {
			return fmt.Errorf("context %q does not define a plugin that supports set", ctx.Name)
		}
		if !pluginHasSet(pluginName) {
			return fmt.Errorf("plugin %q does not support set", pluginName)
		}

		invocation := append([]string{"--context", contextName, "__set"}, filteredArgs...)
		_, err = pluginSDK.InvokePluginCommand(pluginName, invocation, plugin.CommandExecOptions{
			Context: RootCmd.Context(),
			Stdin:   RootCmd.InOrStdin(),
			Stdout:  cmd.OutOrStdout(),
			Stderr:  cmd.ErrOrStderr(),
		})
		if err != nil {
			return cleanPluginCommandError(err)
		}
		return nil
	},
}

func init() {
	setCmd.GroupID = "workflow"
	RootCmd.AddCommand(setCmd)
}

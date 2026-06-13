package cmd

import (
	"fmt"
	"strings"

	"github.com/libops/sitectl/pkg/config"
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
  sitectl set iiif triplet`,
	DisableFlagParsing: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		filteredArgs, contextName, err := getContextFromArgs(cmd, args)
		if err != nil {
			return err
		}
		_, componentArg, ok := firstComponentSetArg(cmd, filteredArgs)
		if !ok {
			return fmt.Errorf("component name is required")
		}

		ctx, err := config.GetContext(contextName)
		if err != nil {
			return err
		}

		pluginName := strings.TrimSpace(ctx.Plugin)
		// Allow plugin/component namespacing: resolve the owning plugin.
		if pluginPart, _, ok := splitNamespacedComponent(componentArg); ok {
			pluginName = pluginPart
		}
		if pluginName == "" || pluginName == "core" {
			return fmt.Errorf("context %q does not define a plugin that supports set", ctx.Name)
		}
		hasSet, err := pluginSupportsSet(pluginName)
		if err != nil {
			return err
		}
		if !hasSet {
			return fmt.Errorf("plugin %q does not support set", pluginName)
		}

		params, pluginArgs, err := extractSetRPCParams(filteredArgs)
		if err != nil {
			return err
		}
		req, err := plugin.NewSetRunRequest(params, pluginArgs...)
		if err != nil {
			return err
		}
		req.Context = contextName
		resp, err := pluginSDK.InvokePluginRPC(pluginName, req, plugin.CommandExecOptions{
			Context:    RootCmd.Context(),
			Stdin:      RootCmd.InOrStdin(),
			Stderr:     cmd.ErrOrStderr(),
			LiveStderr: true,
		})
		if strings.TrimSpace(resp.Output) != "" {
			if _, printErr := fmt.Fprint(cmd.OutOrStdout(), resp.Output); printErr != nil {
				return printErr
			}
		}
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

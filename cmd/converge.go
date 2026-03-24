package cmd

import (
	"fmt"
	"strings"

	"github.com/libops/sitectl/pkg/config"
	"github.com/libops/sitectl/pkg/helpers"
	"github.com/libops/sitectl/pkg/plugin"
	"github.com/spf13/cobra"
)

var convergeCmd = &cobra.Command{
	Use:   "converge [flags]",
	Short: "Detect and repair component configuration drift",
	Long: `Inspect each component registered by the active context's plugin and apply
any changes needed to bring the project back into alignment.

By default the command is interactive and asks before applying changes. Pass
--report to preview what would change without applying it.

This command dispatches to the plugin associated with the active context.
All flags and arguments are forwarded to the plugin's converge handler.

Examples:
  sitectl converge
  sitectl converge --report
  sitectl converge --component fcrepo`,
	DisableFlagParsing: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		filteredArgs, contextName, err := helpers.GetContextFromArgs(cmd, args)
		if err != nil {
			return err
		}

		ctx, err := config.GetContext(contextName)
		if err != nil {
			return err
		}

		pluginName := strings.TrimSpace(ctx.Plugin)
		if pluginName == "" || pluginName == "core" {
			return fmt.Errorf("context %q does not define a plugin that supports converge", ctx.Name)
		}
		if !pluginHasConverge(pluginName) {
			return fmt.Errorf("plugin %q does not support converge", pluginName)
		}

		invocation := append([]string{"--context", contextName, "__converge"}, filteredArgs...)
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
	convergeCmd.GroupID = "workflow"
	RootCmd.AddCommand(convergeCmd)
}

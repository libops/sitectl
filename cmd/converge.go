package cmd

import (
	"fmt"
	"strings"

	"github.com/libops/sitectl/pkg/config"
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
		filteredArgs, contextName, err := getContextFromArgs(cmd, args)
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
		hasConverge, err := pluginSupportsConverge(pluginName)
		if err != nil {
			return err
		}
		if !hasConverge {
			return fmt.Errorf("plugin %q does not support converge", pluginName)
		}

		params, pluginArgs, err := extractConvergeRPCParams(filteredArgs)
		if err != nil {
			return err
		}
		req, err := plugin.NewConvergeRunRequest(params, pluginArgs...)
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
	convergeCmd.GroupID = "workflow"
	RootCmd.AddCommand(convergeCmd)
}

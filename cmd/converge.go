package cmd

import (
	"fmt"
	"strings"

	"github.com/libops/sitectl/pkg/config"
	"github.com/libops/sitectl/pkg/plugin"
	"github.com/spf13/cobra"
)

var convergeCmd = &cobra.Command{
	Use:   "converge",
	Short: "Plan and safely repair component configuration drift",
	Long: `Compare the active project's component-owned configuration with the
durable desired state in .libops/site.yaml, show the complete change plan, and
apply the approved changes needed to restore alignment.

Unknown components, invalid desired state, and changes sitectl cannot classify
block mutation. Destructive changes are identified in the plan. By default each
component change requires confirmation; --yolo skips that safeguard. Pass
--report to inspect the plan without changing files.

The active plugin supplies the component contracts while sitectl uses the same
planning and reconciliation path as component reconcile, validate, and verify.

Examples:
	  sitectl converge
	  sitectl converge --report
	  sitectl converge --component fcrepo
	  sitectl converge --yolo`,
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
		params, pluginArgs, err := extractConvergeRPCParams(filteredArgs)
		if err != nil {
			return err
		}
		req, err := plugin.NewComponentReconcileRequest(plugin.ComponentTargetParams{
			Path:           params.Path,
			CodebaseRootfs: params.CodebaseRootfs,
			Report:         params.Report,
			Verbose:        params.Verbose,
			Format:         params.Format,
		}, pluginArgs...)
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

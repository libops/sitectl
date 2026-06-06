package cmd

import (
	"fmt"
	"strings"

	"github.com/libops/sitectl/pkg/config"
	"github.com/libops/sitectl/pkg/plugin"
	sitevalidate "github.com/libops/sitectl/pkg/validate"
	"github.com/spf13/cobra"
)

var validateCmd = &cobra.Command{
	Use:   "validate [flags]",
	Short: "Validate the context configuration and project layout",
	Long: `Validate the active context's configuration and project layout.

Core checks include: required context fields, compose project presence,
context file accessibility, override symlink, and Docker socket access.

If the active context's plugin registers a validate handler, plugin-specific
checks (e.g. Drupal rootfs path, component state consistency) are also run
and merged into the report.

All flags not consumed by sitectl itself are forwarded to the plugin's
validate handler, allowing plugin-specific flags such as --codebase-rootfs.

Exits non-zero if any check fails.

Examples:
  sitectl validate
  sitectl validate --format table
  sitectl validate --codebase-rootfs drupal/rootfs`,
	DisableFlagParsing: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		filteredArgs, contextName, err := getContextFromArgs(cmd, args)
		if err != nil {
			return err
		}

		validateFormat, validateParams, pluginArgs, err := extractValidateRPCParams(filteredArgs)
		if err != nil {
			return err
		}

		ctxVal, err := config.GetContext(contextName)
		if err != nil {
			return err
		}
		ctx := &ctxVal

		cfg, err := config.Load()
		if err != nil {
			return err
		}

		// Run core validators.
		results, err := sitevalidate.Run(ctx, sitevalidate.CoreValidators(cfg)...)
		if err != nil {
			return err
		}

		// Run plugin validators if the plugin supports validate RPC.
		pluginName := strings.TrimSpace(ctx.Plugin)
		if pluginName != "" && pluginName != "core" {
			hasValidate, err := pluginSupportsValidate(pluginName)
			if err != nil {
				return err
			}
			if hasValidate {
				req, err := plugin.NewValidateRunRequest(validateParams, pluginArgs...)
				if err != nil {
					return err
				}
				req.Context = contextName
				resp, invokeErr := pluginSDK.InvokePluginRPC(pluginName, req, plugin.CommandExecOptions{
					Context: RootCmd.Context(),
				})
				if invokeErr != nil {
					return fmt.Errorf("plugin validate failed: %w", invokeErr)
				}
				if len(resp.Result) != 0 {
					pluginResults, err := plugin.DecodeRPCResult[[]sitevalidate.Result](resp)
					if err != nil {
						return fmt.Errorf("parse plugin validate results: %w", err)
					}
					results = append(results, pluginResults...)
				}
			}
		}

		sitevalidate.SortResults(results)
		report := sitevalidate.NewReport(ctx, results)
		if err := sitevalidate.WriteReports(cmd.OutOrStdout(), []sitevalidate.Report{report}, validateFormat); err != nil {
			return err
		}
		if !report.Valid {
			return fmt.Errorf("validation failed")
		}
		return nil
	},
}

func init() {
	validateCmd.GroupID = "workflow"
	RootCmd.AddCommand(validateCmd)
}

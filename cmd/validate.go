package cmd

import (
	"fmt"
	"strings"

	"github.com/libops/sitectl/pkg/config"
	"github.com/libops/sitectl/pkg/helpers"
	"github.com/libops/sitectl/pkg/plugin"
	sitevalidate "github.com/libops/sitectl/pkg/validate"
	"github.com/spf13/cobra"
	yaml "gopkg.in/yaml.v3"
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
validate handler, allowing plugin-specific flags such as --drupal-rootfs.

Exits non-zero if any check fails.

Examples:
  sitectl validate
  sitectl validate --format table
  sitectl validate --drupal-rootfs drupal/rootfs`,
	DisableFlagParsing: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		filteredArgs, contextName, err := helpers.GetContextFromArgs(cmd, args)
		if err != nil {
			return err
		}

		// Extract --format from remaining args before forwarding to plugin.
		validateFormat, pluginArgs := extractFlag(filteredArgs, "--format")

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

		// Run plugin validators if the plugin supports __validate.
		pluginName := strings.TrimSpace(ctx.Plugin)
		if pluginName != "" && pluginName != "core" && pluginHasValidate(pluginName) {
			invocation := append([]string{"--context", contextName, "__validate"}, pluginArgs...)
			output, invokeErr := pluginSDK.InvokePluginCommand(pluginName, invocation, plugin.CommandExecOptions{
				Context: RootCmd.Context(),
				Capture: true,
			})
			if invokeErr != nil {
				return fmt.Errorf("plugin validate failed: %w", invokeErr)
			}
			if trimmed := strings.TrimSpace(output); trimmed != "" {
				var pluginResults []sitevalidate.Result
				if err := yaml.Unmarshal([]byte(trimmed), &pluginResults); err != nil {
					return fmt.Errorf("parse plugin validate results: %w", err)
				}
				results = append(results, pluginResults...)
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

// extractFlag removes --flag and its value from args and returns (value, remaining).
// Handles both "--flag value" and "--flag=value" forms.
func extractFlag(args []string, flag string) (string, []string) {
	value := ""
	remaining := make([]string, 0, len(args))
	skipNext := false
	for _, arg := range args {
		if skipNext {
			value = arg
			skipNext = false
			continue
		}
		if arg == flag {
			skipNext = true
			continue
		}
		if strings.HasPrefix(arg, flag+"=") {
			value = strings.TrimPrefix(arg, flag+"=")
			continue
		}
		remaining = append(remaining, arg)
	}
	return value, remaining
}

func init() {
	validateCmd.GroupID = "workflow"
	RootCmd.AddCommand(validateCmd)
}

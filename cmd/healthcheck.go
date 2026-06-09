package cmd

import (
	"fmt"
	"strings"
	"time"

	"github.com/libops/sitectl/pkg/config"
	"github.com/libops/sitectl/pkg/healthcheck"
	"github.com/libops/sitectl/pkg/plugin"
	sitevalidate "github.com/libops/sitectl/pkg/validate"
	"github.com/spf13/cobra"
)

var healthcheckCmd = &cobra.Command{
	Use:   "healthcheck [flags]",
	Short: "Check whether the active site is online",
	Long: `Check whether the active site is online.

Core checks verify Docker Compose service containers are running and healthy.
If the active context's plugin registers a healthcheck handler, plugin-specific
runtime checks are also run and merged into the report.

All flags not consumed by sitectl itself are forwarded to the plugin's
healthcheck handler, allowing plugin-specific flags such as --codebase-rootfs.

By default, healthcheck runs once, prints the current status of each check, and
exits non-zero if any check fails. Use --persist to keep retrying until all
checks pass or --timeout is reached.

Examples:
  sitectl healthcheck
  sitectl healthcheck --persist --timeout 10m --interval 15s
  sitectl healthcheck --format table`,
	DisableFlagParsing: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		filteredArgs, contextName, err := getContextFromArgs(cmd, args)
		if err != nil {
			return err
		}

		hostParams, healthcheckParams, pluginArgs, err := extractHealthcheckRPCParams(filteredArgs)
		if err != nil {
			return err
		}
		if hostParams.Persist && hostParams.Interval <= 0 {
			return fmt.Errorf("--interval must be greater than zero")
		}

		ctxVal, err := config.GetContext(contextName)
		if err != nil {
			return err
		}
		ctx := &ctxVal

		report, err := runHealthcheckReport(cmd, ctx, contextName, hostParams, healthcheckParams, pluginArgs)
		if err != nil {
			return err
		}
		if err := sitevalidate.WriteReports(cmd.OutOrStdout(), []sitevalidate.Report{report}, hostParams.Format); err != nil {
			return err
		}
		if !report.Valid {
			return fmt.Errorf("healthcheck failed")
		}
		return nil
	},
}

func init() {
	healthcheckCmd.GroupID = "workflow"
	RootCmd.AddCommand(healthcheckCmd)
}

func runHealthcheckReport(cmd *cobra.Command, ctx *config.Context, contextName string, hostParams healthcheckHostParams, healthcheckParams plugin.HealthcheckRunParams, pluginArgs []string) (sitevalidate.Report, error) {
	if hostParams.Persist {
		return runHealthcheckUntilHealthy(cmd, ctx, contextName, hostParams, healthcheckParams, pluginArgs)
	}

	results, err := runHealthcheckOnce(cmd, ctx, contextName, healthcheckParams, pluginArgs)
	if err != nil {
		return sitevalidate.Report{}, err
	}
	sitevalidate.SortResults(results)
	return sitevalidate.NewReport(ctx, results), nil
}

func runHealthcheckUntilHealthy(cmd *cobra.Command, ctx *config.Context, contextName string, hostParams healthcheckHostParams, healthcheckParams plugin.HealthcheckRunParams, pluginArgs []string) (sitevalidate.Report, error) {
	deadline := time.Now().Add(hostParams.Timeout)
	var last sitevalidate.Report
	for {
		results, err := runHealthcheckOnce(cmd, ctx, contextName, healthcheckParams, pluginArgs)
		if err != nil {
			return sitevalidate.Report{}, err
		}
		sitevalidate.SortResults(results)
		last = sitevalidate.NewReport(ctx, results)
		if last.Valid || hostParams.Timeout <= 0 || time.Now().Add(hostParams.Interval).After(deadline) {
			return last, nil
		}
		timer := time.NewTimer(hostParams.Interval)
		select {
		case <-cmd.Context().Done():
			timer.Stop()
			return sitevalidate.Report{}, cmd.Context().Err()
		case <-timer.C:
		}
	}
}

func runHealthcheckOnce(cmd *cobra.Command, ctx *config.Context, contextName string, healthcheckParams plugin.HealthcheckRunParams, pluginArgs []string) ([]sitevalidate.Result, error) {
	checker, err := healthcheck.NewDockerChecker(ctx)
	if err != nil {
		return nil, err
	}
	defer checker.Close()

	results, err := checker.CheckComposeServices(cmd.Context())
	if err != nil {
		return nil, err
	}

	pluginName := strings.TrimSpace(ctx.Plugin)
	if pluginName == "" || pluginName == "core" {
		return results, nil
	}
	hasHealthcheck, err := pluginSupportsHealthcheck(pluginName)
	if err != nil {
		return nil, err
	}
	if !hasHealthcheck {
		return results, nil
	}
	req, err := plugin.NewHealthcheckRunRequest(healthcheckParams, pluginArgs...)
	if err != nil {
		return nil, err
	}
	req.Context = contextName
	resp, invokeErr := pluginSDK.InvokePluginRPC(pluginName, req, plugin.CommandExecOptions{
		Context: cmd.Context(),
	})
	if invokeErr != nil {
		return nil, fmt.Errorf("plugin healthcheck failed: %w", invokeErr)
	}
	if len(resp.Result) == 0 {
		return results, nil
	}
	pluginResults, err := plugin.DecodeRPCResult[[]sitevalidate.Result](resp)
	if err != nil {
		return nil, fmt.Errorf("parse plugin healthcheck results: %w", err)
	}
	return append(results, pluginResults...), nil
}

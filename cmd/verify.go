package cmd

import (
	"fmt"
	"strings"

	"github.com/libops/sitectl/pkg/config"
	"github.com/libops/sitectl/pkg/plugin"
	sitevalidate "github.com/libops/sitectl/pkg/validate"
	"github.com/spf13/cobra"
)

var verifyCmd = &cobra.Command{
	Use:   "verify",
	Short: "Run plugin verification checks for the active site",
	Long: `Run plugin verification checks for the active site.

Verification checks are deeper behavioral checks implemented by the active
context's plugin. They are intended for CI, preview, development, and staging
deployments after healthcheck has already confirmed the site is online.

Before reporting success, verify consumes the same component reconciliation plan
as converge and validate. Behavioral checks cannot hide missing desired state,
unknown component ownership, or unapplied component changes.

All flags not consumed by sitectl itself are forwarded to the plugin's verify
handler, allowing plugin-specific flags such as --fcrepo or --bot-mitigation.
Use --strict in release and smoke-test automation to fail when the active
plugin has no verification checks, returns no check results, or reports a
warning that requires operator review.

Examples:
  sitectl verify
  sitectl verify --format table
  sitectl verify --strict
  sitectl verify --bot-mitigation on`,
	DisableFlagParsing: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		filteredArgs, contextName, err := getContextFromArgs(cmd, args)
		if err != nil {
			return err
		}
		strict, filteredArgs, err := extractVerifyStrict(filteredArgs)
		if err != nil {
			return err
		}

		verifyFormat, verifyParams, pluginArgs, err := extractVerifyRPCParams(filteredArgs)
		if err != nil {
			return err
		}

		ctxVal, err := config.GetContext(contextName)
		if err != nil {
			return err
		}
		ctx := &ctxVal

		results, err := runVerifyResults(cmd, ctx, contextName, verifyParams, pluginArgs, strict)
		if err != nil {
			return err
		}
		plan, err := loadReconciliationPlan(cmd, contextName, strings.TrimSpace(ctx.Plugin), "")
		if err != nil {
			return err
		}
		results = append(results, reconciliationValidationResults(plan)...)
		results = enforceStrictVerifyResults(results, strict)
		sitevalidate.SortResults(results)
		report := sitevalidate.NewReport(ctx, results)
		if err := sitevalidate.WriteReports(cmd.OutOrStdout(), []sitevalidate.Report{report}, verifyFormat); err != nil {
			return err
		}
		if !report.Valid {
			return fmt.Errorf("verification failed")
		}
		return nil
	},
}

func init() {
	verifyCmd.GroupID = "workflow"
	RootCmd.AddCommand(verifyCmd)
}

func runVerifyResults(cmd *cobra.Command, ctx *config.Context, contextName string, verifyParams plugin.VerifyRunParams, pluginArgs []string, strict bool) ([]sitevalidate.Result, error) {
	pluginName := strings.TrimSpace(ctx.Plugin)
	if pluginName == "" || pluginName == "core" {
		return []sitevalidate.Result{unavailableVerifyResult("verify", "no plugin-specific verify runner is available for this context", strict)}, nil
	}

	hasVerify, err := pluginSupportsVerify(pluginName)
	if err != nil {
		return nil, err
	}
	if !hasVerify {
		return []sitevalidate.Result{unavailableVerifyResult("verify:"+pluginName, "plugin does not provide verification checks", strict)}, nil
	}

	req, err := plugin.NewVerifyRunRequest(verifyParams, pluginArgs...)
	if err != nil {
		return nil, err
	}
	req.Context = contextName
	resp, invokeErr := pluginSDK.InvokePluginRPC(pluginName, req, plugin.CommandExecOptions{
		Context: cmd.Context(),
	})
	if invokeErr != nil {
		return nil, fmt.Errorf("plugin verify failed: %w", invokeErr)
	}
	if len(resp.Result) == 0 {
		return []sitevalidate.Result{unavailableVerifyResult("verify:"+pluginName, "plugin returned no verification results", strict)}, nil
	}
	results, err := plugin.DecodeRPCResult[[]sitevalidate.Result](resp)
	if err != nil {
		return nil, fmt.Errorf("parse plugin verify results: %w", err)
	}
	if len(results) == 0 {
		results = append(results, unavailableVerifyResult("verify:"+pluginName, "plugin returned no verification results", strict))
	}
	return results, nil
}

func unavailableVerifyResult(name, detail string, strict bool) sitevalidate.Result {
	status := sitevalidate.StatusWarning
	if strict {
		status = sitevalidate.StatusFailed
	}
	return sitevalidate.Result{Name: name, Status: status, Detail: detail}
}

func enforceStrictVerifyResults(results []sitevalidate.Result, strict bool) []sitevalidate.Result {
	if !strict {
		return results
	}
	for i := range results {
		if results[i].Status == sitevalidate.StatusWarning {
			results[i].Status = sitevalidate.StatusFailed
		}
	}
	return results
}

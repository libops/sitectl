package cmd

import (
	"encoding/json"
	"fmt"
	"strings"

	corecomponent "github.com/libops/sitectl/pkg/component"
	"github.com/libops/sitectl/pkg/plugin"
	sitevalidate "github.com/libops/sitectl/pkg/validate"
	"github.com/spf13/cobra"
)

func loadReconciliationPlan(cmd *cobra.Command, contextName, pluginName, codebaseRootfs string) (*corecomponent.ReconciliationPlan, error) {
	if strings.TrimSpace(pluginName) == "" || strings.TrimSpace(pluginName) == "core" {
		return nil, nil
	}
	req, err := plugin.NewComponentReconcileRequest(plugin.ComponentTargetParams{
		CodebaseRootfs: codebaseRootfs,
		Report:         true,
		Format:         corecomponent.ReportFormatJSON,
	})
	if err != nil {
		return nil, err
	}
	req.Context = contextName
	resp, invokeErr := pluginSDK.InvokePluginRPC(pluginName, req, plugin.CommandExecOptions{Context: cmd.Context()})
	if invokeErr != nil {
		if strings.Contains(strings.ToLower(invokeErr.Error()), "not registered") ||
			strings.Contains(strings.ToLower(invokeErr.Error()), "unsupported") {
			return nil, nil
		}
		return nil, fmt.Errorf("build component reconciliation plan: %w", invokeErr)
	}
	if strings.TrimSpace(resp.Output) == "" {
		return nil, nil
	}
	var plan corecomponent.ReconciliationPlan
	if err := json.Unmarshal([]byte(resp.Output), &plan); err != nil {
		return nil, fmt.Errorf("plugin %q returned an invalid reconciliation plan: %w", pluginName, err)
	}
	return &plan, nil
}

func reconciliationValidationResults(plan *corecomponent.ReconciliationPlan) []sitevalidate.Result {
	if plan == nil {
		return nil
	}
	results := []sitevalidate.Result{}
	if !plan.Safe {
		results = append(results, sitevalidate.Result{
			Name:    "component-desired-state",
			Status:  sitevalidate.StatusFailed,
			Detail:  "component intent cannot be reproduced safely",
			FixHint: "resolve every unknown reported by sitectl converge --report",
		})
	}
	for _, unknown := range plan.Unknowns {
		results = append(results, sitevalidate.Result{Name: "component-unknown", Status: sitevalidate.StatusFailed, Detail: unknown})
	}
	for _, item := range plan.Components {
		for _, unknown := range item.Unknowns {
			results = append(results, sitevalidate.Result{
				Name:   "component:" + item.Name,
				Status: sitevalidate.StatusFailed,
				Detail: unknown,
			})
		}
		if len(item.Unknowns) == 0 && !item.InSync {
			results = append(results, sitevalidate.Result{
				Name:    "component:" + item.Name,
				Status:  sitevalidate.StatusFailed,
				Detail:  fmt.Sprintf("%d component-owned change(s) differ from desired disposition %s", len(item.Changes), item.Desired),
				FixHint: "review with sitectl converge --report, then run sitectl converge",
			})
		}
	}
	if len(results) == 0 {
		results = append(results, sitevalidate.Result{Name: "component-desired-state", Status: sitevalidate.StatusOK})
	}
	return results
}

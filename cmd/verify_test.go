package cmd

import (
	"testing"

	"github.com/libops/sitectl/pkg/config"
	"github.com/libops/sitectl/pkg/plugin"
	sitevalidate "github.com/libops/sitectl/pkg/validate"
	"github.com/spf13/cobra"
)

func TestRunVerifyResultsStrictFailsWithoutPluginChecks(t *testing.T) {
	results, err := runVerifyResults(&cobra.Command{}, &config.Context{Plugin: "core"}, "test", plugin.VerifyRunParams{}, nil, true)
	if err != nil {
		t.Fatalf("runVerifyResults() error = %v", err)
	}
	if len(results) != 1 || results[0].Status != sitevalidate.StatusFailed {
		t.Fatalf("results = %+v, want one failed result", results)
	}
}

func TestRunVerifyResultsDefaultWarnsWithoutPluginChecks(t *testing.T) {
	results, err := runVerifyResults(&cobra.Command{}, &config.Context{Plugin: "core"}, "test", plugin.VerifyRunParams{}, nil, false)
	if err != nil {
		t.Fatalf("runVerifyResults() error = %v", err)
	}
	if len(results) != 1 || results[0].Status != sitevalidate.StatusWarning {
		t.Fatalf("results = %+v, want one warning result", results)
	}
}

func TestEnforceStrictVerifyResultsPromotesWarnings(t *testing.T) {
	results := enforceStrictVerifyResults([]sitevalidate.Result{
		{Name: "ok", Status: sitevalidate.StatusOK},
		{Name: "review", Status: sitevalidate.StatusWarning, Detail: "operator decision required"},
	}, true)
	if results[0].Status != sitevalidate.StatusOK {
		t.Fatalf("OK result changed in strict mode: %+v", results[0])
	}
	if results[1].Status != sitevalidate.StatusFailed || results[1].Detail != "operator decision required" {
		t.Fatalf("warning was not promoted without losing detail: %+v", results[1])
	}
}

func TestEnforceStrictVerifyResultsLeavesInteractiveWarnings(t *testing.T) {
	results := enforceStrictVerifyResults([]sitevalidate.Result{{Name: "review", Status: sitevalidate.StatusWarning}}, false)
	if results[0].Status != sitevalidate.StatusWarning {
		t.Fatalf("non-strict warning changed: %+v", results[0])
	}
}

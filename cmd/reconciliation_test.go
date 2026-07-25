package cmd

import (
	"testing"

	corecomponent "github.com/libops/sitectl/pkg/component"
	sitevalidate "github.com/libops/sitectl/pkg/validate"
)

func TestReconciliationValidationResultsBlocksDriftAndUnknowns(t *testing.T) {
	t.Parallel()
	plan := &corecomponent.ReconciliationPlan{
		Safe: false,
		Components: []corecomponent.ComponentPlan{{
			Name:     "search",
			Desired:  corecomponent.DispositionEnabled,
			InSync:   false,
			Changes:  []corecomponent.PlannedChange{{Path: "services.search"}},
			Unknowns: []string{"ownership is ambiguous"},
		}},
	}
	results := reconciliationValidationResults(plan)
	if len(results) < 2 {
		t.Fatalf("results = %#v", results)
	}
	for _, result := range results {
		if result.Status != sitevalidate.StatusFailed {
			t.Fatalf("result = %#v, want failed", result)
		}
	}
}

func TestReconciliationValidationResultsAcceptsInSyncPlan(t *testing.T) {
	t.Parallel()
	results := reconciliationValidationResults(&corecomponent.ReconciliationPlan{Safe: true, InSync: true})
	if len(results) != 1 || results[0].Status != sitevalidate.StatusOK {
		t.Fatalf("results = %#v", results)
	}
}

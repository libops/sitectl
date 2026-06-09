package cmd

import (
	"errors"
	"testing"

	"github.com/libops/sitectl/pkg/plugin"
)

func TestCleanPluginCommandErrorUsesProcessDetail(t *testing.T) {
	err := cleanPluginCommandError(&plugin.RPCProcessError{
		Plugin:   "custom",
		Method:   plugin.MethodJobRun,
		ExitCode: 2,
		Detail:   "clean failure",
		Err:      errors.New("exit status 2"),
	})
	if err.Error() != "clean failure" {
		t.Fatalf("expected clean plugin error, got %v", err)
	}
}

func TestCleanPluginCommandErrorLeavesUnmatchedErrors(t *testing.T) {
	original := errors.New("plain failure")
	if got := cleanPluginCommandError(original); got != original {
		t.Fatalf("expected unmatched error to be preserved, got %v", got)
	}
}

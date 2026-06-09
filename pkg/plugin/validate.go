package plugin

import (
	"github.com/libops/sitectl/pkg/config"
	sitevalidate "github.com/libops/sitectl/pkg/validate"
	"github.com/spf13/cobra"
)

// ValidateRunner implements plugin-specific context validation.
// Run returns a list of validation results for the active context. Diagnostics
// that should be visible to users should be written to cmd.ErrOrStderr();
// stdout is captured by the RPC envelope and may not be displayed by callers.
type ValidateRunner interface {
	BindFlags(cmd *cobra.Command)
	Run(cmd *cobra.Command, ctx *config.Context) ([]sitevalidate.Result, error)
}

// RegisterValidateRunner registers a validate runner for the plugin. The SDK
// stores the validate handler that is invoked through the plugin RPC entrypoint.
// The handler output is encoded into the RPC result and merged with core
// validation results before writing the final report. BindFlags must declare
// the RPC-bridged --codebase-rootfs flag. If BindFlags uses a plugin-specific
// flag for CodebaseRootfs params, mark it with MarkCodebaseRootfsFlag.
// Registration panics if required bridge flags are missing.
func (s *SDK) RegisterValidateRunner(runner ValidateRunner) {
	if s == nil || runner == nil {
		return
	}
	cmd := &cobra.Command{
		Use:          "validate",
		Short:        "Internal validate hook",
		Hidden:       true,
		SilenceUsage: true,
	}
	runner.BindFlags(cmd)
	s.registerValidateCommand(cmd)
	s.validateRunner = runner
	s.hasValidate = true
}

func (s *SDK) runValidateRunner(cmd *cobra.Command, runner ValidateRunner) ([]sitevalidate.Result, error) {
	ctx, err := s.GetContext()
	if err != nil {
		return nil, err
	}
	return runner.Run(cmd, ctx)
}

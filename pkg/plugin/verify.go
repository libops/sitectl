package plugin

import (
	"github.com/libops/sitectl/pkg/config"
	sitevalidate "github.com/libops/sitectl/pkg/validate"
	"github.com/spf13/cobra"
)

// VerifyRunner implements plugin-specific behavioral verification checks.
// Run returns a list of verification results for the active context.
// Diagnostics that should be visible to users should be written to
// cmd.ErrOrStderr(); stdout is captured by the RPC envelope and may not be
// displayed by callers.
type VerifyRunner interface {
	BindFlags(cmd *cobra.Command)
	Run(cmd *cobra.Command, ctx *config.Context) ([]sitevalidate.Result, error)
}

// RegisterVerifyRunner registers a verify runner for the plugin. The SDK
// stores the handler that is invoked through the plugin RPC entrypoint. The
// handler output is encoded into the RPC result and written by the host
// sitectl verify command.
func (s *SDK) RegisterVerifyRunner(runner VerifyRunner) {
	if s == nil || runner == nil {
		return
	}
	cmd := &cobra.Command{
		Use:          "verify",
		Short:        "Internal verify hook",
		Hidden:       true,
		SilenceUsage: true,
	}
	runner.BindFlags(cmd)
	s.registerVerifyCommand(cmd)
	s.verifyRunner = runner
	s.hasVerify = true
}

func (s *SDK) runVerifyRunner(cmd *cobra.Command, runner VerifyRunner) ([]sitevalidate.Result, error) {
	ctx, err := s.GetContext()
	if err != nil {
		return nil, err
	}
	return runner.Run(cmd, ctx)
}

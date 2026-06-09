package plugin

import (
	"github.com/libops/sitectl/pkg/config"
	sitevalidate "github.com/libops/sitectl/pkg/validate"
	"github.com/spf13/cobra"
)

// HealthcheckRunner implements plugin-specific runtime health checks.
// Run returns a list of healthcheck results for the active context. Diagnostics
// that should be visible to users should be written to cmd.ErrOrStderr();
// stdout is captured by the RPC envelope and may not be displayed by callers.
type HealthcheckRunner interface {
	BindFlags(cmd *cobra.Command)
	Run(cmd *cobra.Command, ctx *config.Context) ([]sitevalidate.Result, error)
}

// RegisterHealthcheckRunner registers a healthcheck runner for the plugin. The
// SDK stores the handler that is invoked through the plugin RPC entrypoint. The
// handler output is encoded into the RPC result and merged with core health
// results before writing the final report. If BindFlags uses a plugin-specific
// flag for CodebaseRootfs params, mark it with MarkCodebaseRootfsFlag.
func (s *SDK) RegisterHealthcheckRunner(runner HealthcheckRunner) {
	if s == nil || runner == nil {
		return
	}
	cmd := &cobra.Command{
		Use:          "healthcheck",
		Short:        "Internal healthcheck hook",
		Hidden:       true,
		SilenceUsage: true,
	}
	runner.BindFlags(cmd)
	s.registerHealthcheckCommand(cmd)
	s.healthcheckRunner = runner
	s.hasHealthcheck = true
}

func (s *SDK) runHealthcheckRunner(cmd *cobra.Command, runner HealthcheckRunner) ([]sitevalidate.Result, error) {
	ctx, err := s.GetContext()
	if err != nil {
		return nil, err
	}
	return runner.Run(cmd, ctx)
}

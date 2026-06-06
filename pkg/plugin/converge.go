package plugin

import (
	"github.com/libops/sitectl/pkg/config"
	"github.com/spf13/cobra"
)

// ConvergeRunner implements plugin-specific component convergence.
// Run detects and repairs configuration drift for the active context.
type ConvergeRunner interface {
	BindFlags(cmd *cobra.Command)
	Run(cmd *cobra.Command, ctx *config.Context) error
}

// RegisterConvergeRunner registers a converge runner for the plugin. The SDK
// invokes it through the plugin RPC entrypoint for sitectl converge. BindFlags
// must declare the RPC-bridged --path, --codebase-rootfs, --report, --verbose,
// and --format flags. If BindFlags uses a plugin-specific flag for
// CodebaseRootfs params, mark it with MarkCodebaseRootfsFlag. Registration
// panics if required bridge flags are missing.
func (s *SDK) RegisterConvergeRunner(runner ConvergeRunner) {
	if s == nil || runner == nil {
		return
	}
	cmd := &cobra.Command{
		Use:          "converge",
		Short:        "Internal converge hook",
		Hidden:       true,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := s.GetContext()
			if err != nil {
				return err
			}
			return runner.Run(cmd, ctx)
		},
	}
	runner.BindFlags(cmd)
	s.registerConvergeCommand(cmd)
	s.hasConverge = true
}

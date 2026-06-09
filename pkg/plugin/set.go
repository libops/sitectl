package plugin

import (
	"github.com/libops/sitectl/pkg/config"
	"github.com/spf13/cobra"
)

// SetRunner implements plugin-specific component state management.
// Run applies the requested state or disposition to the named component.
type SetRunner interface {
	BindFlags(cmd *cobra.Command)
	Run(cmd *cobra.Command, args []string, ctx *config.Context) error
}

// RegisterSetRunner registers a set runner for the plugin. The SDK invokes it
// through the plugin RPC entrypoint for sitectl set. BindFlags must declare the
// RPC-bridged --path flag; registration panics if required bridge flags are
// missing.
func (s *SDK) RegisterSetRunner(runner SetRunner) {
	if s == nil || runner == nil {
		return
	}
	cmd := &cobra.Command{
		Use:          "set",
		Short:        "Internal set hook",
		Hidden:       true,
		SilenceUsage: true,
		Args:         cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := s.GetContext()
			if err != nil {
				return err
			}
			return runner.Run(cmd, args, ctx)
		},
	}
	runner.BindFlags(cmd)
	s.registerSetCommand(cmd)
	s.hasSet = true
}

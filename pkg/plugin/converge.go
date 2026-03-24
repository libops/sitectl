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
// creates the __converge hidden command that is invoked by sitectl converge.
func (s *SDK) RegisterConvergeRunner(runner ConvergeRunner) {
	if s == nil || runner == nil {
		return
	}
	cmd := &cobra.Command{
		Use:          "__converge",
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
	s.RootCmd.AddCommand(cmd)
	s.hasConverge = true
}

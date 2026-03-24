package plugin

import (
	"github.com/libops/sitectl/pkg/config"
	sitevalidate "github.com/libops/sitectl/pkg/validate"
	"github.com/spf13/cobra"
	yaml "gopkg.in/yaml.v3"
)

// ValidateRunner implements plugin-specific context validation.
// Run returns a list of validation results for the active context.
type ValidateRunner interface {
	BindFlags(cmd *cobra.Command)
	Run(cmd *cobra.Command, ctx *config.Context) ([]sitevalidate.Result, error)
}

// RegisterValidateRunner registers a validate runner for the plugin. The SDK
// creates the __validate hidden command that is invoked by sitectl validate.
// The command outputs YAML-encoded []validate.Result that sitectl merges with
// core validation results before writing the final report.
func (s *SDK) RegisterValidateRunner(runner ValidateRunner) {
	if s == nil || runner == nil {
		return
	}
	cmd := &cobra.Command{
		Use:          "__validate",
		Short:        "Internal validate hook",
		Hidden:       true,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := s.GetContext()
			if err != nil {
				return err
			}
			results, err := runner.Run(cmd, ctx)
			if err != nil {
				return err
			}
			data, err := yaml.Marshal(results)
			if err != nil {
				return err
			}
			_, err = cmd.OutOrStdout().Write(data)
			return err
		},
	}
	runner.BindFlags(cmd)
	s.RootCmd.AddCommand(cmd)
	s.hasValidate = true
}

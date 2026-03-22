package plugin

import (
	"fmt"
	"strings"

	"github.com/libops/sitectl/pkg/config"
	"github.com/libops/sitectl/pkg/job"
	"github.com/spf13/cobra"
	yaml "gopkg.in/yaml.v3"
)

type RegisteredJob struct {
	Spec    job.Spec
	Command *cobra.Command
}

type ContextJob interface {
	BindFlags(cmd *cobra.Command)
	Run(cmd *cobra.Command, ctx *config.Context) error
}

func (s *SDK) RegisterJob(spec job.Spec, cmd *cobra.Command) {
	if s == nil || cmd == nil {
		return
	}
	root := s.ensureJobRoot()
	if strings.TrimSpace(spec.Name) == "" {
		spec.Name = strings.TrimSpace(cmd.Use)
	}
	if strings.TrimSpace(spec.Plugin) == "" {
		spec.Plugin = s.Metadata.Name
	}
	if strings.TrimSpace(spec.Name) == "" {
		return
	}
	cmd.Use = spec.Name
	cmd.Hidden = true
	if cmd.Short == "" {
		cmd.Short = spec.Description
	}
	root.AddCommand(cmd)
	s.jobs = append(s.jobs, RegisteredJob{Spec: spec, Command: cmd})
}

func (s *SDK) RegisterContextJob(spec job.Spec, runner ContextJob) {
	if s == nil || runner == nil {
		return
	}
	cmd := &cobra.Command{
		Use:          strings.TrimSpace(spec.Name),
		Short:        spec.Description,
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
	s.RegisterJob(spec, cmd)
}

func (s *SDK) ensureJobRoot() *cobra.Command {
	if s.jobRootCmd != nil {
		return s.jobRootCmd
	}
	root := &cobra.Command{
		Use:          "__job",
		Hidden:       true,
		SilenceUsage: true,
	}
	listCmd := &cobra.Command{
		Use:    "list",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			specs := make([]job.Spec, 0, len(s.jobs))
			for _, registered := range s.jobs {
				specs = append(specs, registered.Spec)
			}
			data, err := yaml.Marshal(specs)
			if err != nil {
				return fmt.Errorf("marshal jobs: %w", err)
			}
			_, err = cmd.OutOrStdout().Write(data)
			return err
		},
	}
	root.AddCommand(listCmd)
	s.jobRootCmd = root
	s.RootCmd.AddCommand(root)
	return root
}

package plugin

import (
	"fmt"
	"strings"

	"github.com/libops/sitectl/pkg/config"
	"github.com/spf13/cobra"
	yaml "gopkg.in/yaml.v3"
)

// DeploySpec describes a plugin's deploy capability.
type DeploySpec struct {
	Name        string `yaml:"name"`
	Plugin      string `yaml:"plugin,omitempty"`
	Description string `yaml:"description,omitempty"`
	Default     bool   `yaml:"default,omitempty"`
}

// RegisteredDeploy holds a registered deploy spec.
type RegisteredDeploy struct {
	Spec DeploySpec
}

// DeployRunner implements plugin-specific lifecycle hooks for the deploy flow.
// PreDown runs before compose down; PostUp runs after compose up.
type DeployRunner interface {
	BindFlags(cmd *cobra.Command)
	PreDown(cmd *cobra.Command, ctx *config.Context) error
	PostUp(cmd *cobra.Command, ctx *config.Context) error
}

// RegisterDeployRunner registers a deploy runner for the plugin. The SDK creates
// the __deploy hidden command with pre-down and post-up subcommands that are
// invoked by sitectl deploy around the compose down/up cycle.
func (s *SDK) RegisterDeployRunner(spec DeploySpec, runner DeployRunner) {
	if s == nil || runner == nil {
		return
	}
	spec = normalizeDeploySpec(spec)
	if strings.TrimSpace(spec.Name) == "" {
		return
	}
	if strings.TrimSpace(spec.Plugin) == "" {
		spec.Plugin = s.Metadata.Name
	}

	root := s.ensureDeployRoot()

	preDownCmd := &cobra.Command{
		Use:          "pre-down",
		Short:        "Run pre-down lifecycle hooks before compose down",
		Hidden:       true,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := s.GetContext()
			if err != nil {
				return err
			}
			return runner.PreDown(cmd, ctx)
		},
	}
	postUpCmd := &cobra.Command{
		Use:          "post-up",
		Short:        "Run post-up lifecycle hooks after compose up",
		Hidden:       true,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := s.GetContext()
			if err != nil {
				return err
			}
			return runner.PostUp(cmd, ctx)
		},
	}
	runner.BindFlags(preDownCmd)
	runner.BindFlags(postUpCmd)

	root.AddCommand(preDownCmd)
	root.AddCommand(postUpCmd)
	s.deploys = append(s.deploys, RegisteredDeploy{Spec: spec})
}

// DeployDefinitions returns the deploy specs registered with this SDK instance.
func (s *SDK) DeployDefinitions() []DeploySpec {
	if s == nil {
		return nil
	}
	out := make([]DeploySpec, 0, len(s.deploys))
	for _, registered := range s.deploys {
		out = append(out, registered.Spec)
	}
	return out
}

func (s *SDK) ensureDeployRoot() *cobra.Command {
	if s.deployRootCmd != nil {
		return s.deployRootCmd
	}
	root := &cobra.Command{
		Use:          "__deploy",
		Hidden:       true,
		SilenceUsage: true,
	}
	listCmd := &cobra.Command{
		Use:    "list",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			specs := s.DeployDefinitions()
			data, err := yaml.Marshal(specs)
			if err != nil {
				return fmt.Errorf("marshal deploys: %w", err)
			}
			_, err = cmd.OutOrStdout().Write(data)
			return err
		},
	}
	root.AddCommand(listCmd)
	s.deployRootCmd = root
	s.RootCmd.AddCommand(root)
	return root
}

func normalizeDeploySpec(spec DeploySpec) DeploySpec {
	spec.Name = strings.TrimSpace(spec.Name)
	spec.Plugin = strings.TrimSpace(spec.Plugin)
	spec.Description = strings.TrimSpace(spec.Description)
	return spec
}

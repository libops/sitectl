package plugin

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	yaml "gopkg.in/yaml.v3"
)

type CreateSpec struct {
	Name                string   `yaml:"name"`
	Plugin              string   `yaml:"plugin,omitempty"`
	Description         string   `yaml:"description,omitempty"`
	Default             bool     `yaml:"default,omitempty"`
	MinCPUCores         float64  `yaml:"min_cpu_cores,omitempty"`
	MinMemory           string   `yaml:"min_memory,omitempty"`
	MinDiskSpace        string   `yaml:"min_disk_space,omitempty"`
	DockerComposeRepo   string   `yaml:"docker_compose_repo,omitempty"`
	DockerComposeBranch string   `yaml:"docker_compose_branch,omitempty"`
	DockerComposeInit   []string `yaml:"docker_compose_init,omitempty"`
	DockerComposeUp     []string `yaml:"docker_compose_up,omitempty"`
	DockerComposeDown   []string `yaml:"docker_compose_down,omitempty"`
}

type RegisteredCreate struct {
	Spec    CreateSpec
	Command *cobra.Command
}

type CreateRunner interface {
	BindFlags(cmd *cobra.Command)
	Run(cmd *cobra.Command) error
}

func (s *SDK) RegisterCreate(spec CreateSpec, cmd *cobra.Command) {
	if s == nil || cmd == nil {
		return
	}
	root := s.ensureCreateRoot()
	spec = normalizeCreateSpec(spec)
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
	s.creates = append(s.creates, RegisteredCreate{Spec: spec, Command: cmd})
}

func (s *SDK) RegisterCreateRunner(spec CreateSpec, runner CreateRunner) {
	if s == nil || runner == nil {
		return
	}
	spec = normalizeCreateSpec(spec)
	cmd := &cobra.Command{
		Use:          strings.TrimSpace(spec.Name),
		Short:        spec.Description,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runner.Run(cmd)
		},
	}
	runner.BindFlags(cmd)
	s.RegisterCreate(spec, cmd)
}

func (s *SDK) CreateDefinitions() []CreateSpec {
	if s == nil {
		return nil
	}
	out := make([]CreateSpec, 0, len(s.creates))
	for _, registered := range s.creates {
		out = append(out, registered.Spec)
	}
	return out
}

func (s *SDK) ensureCreateRoot() *cobra.Command {
	if s.createRootCmd != nil {
		return s.createRootCmd
	}
	root := &cobra.Command{
		Use:          "__create",
		Hidden:       true,
		SilenceUsage: true,
	}
	listCmd := &cobra.Command{
		Use:    "list",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			specs := s.CreateDefinitions()
			data, err := yaml.Marshal(specs)
			if err != nil {
				return fmt.Errorf("marshal creates: %w", err)
			}
			_, err = cmd.OutOrStdout().Write(data)
			return err
		},
	}
	root.AddCommand(listCmd)
	s.createRootCmd = root
	s.RootCmd.AddCommand(root)
	return root
}

func normalizeCreateSpec(spec CreateSpec) CreateSpec {
	spec.Name = strings.TrimSpace(spec.Name)
	spec.Plugin = strings.TrimSpace(spec.Plugin)
	spec.Description = strings.TrimSpace(spec.Description)
	spec.MinMemory = strings.TrimSpace(spec.MinMemory)
	spec.MinDiskSpace = strings.TrimSpace(spec.MinDiskSpace)
	spec.DockerComposeRepo = strings.TrimSpace(spec.DockerComposeRepo)
	spec.DockerComposeBranch = strings.TrimSpace(spec.DockerComposeBranch)
	if spec.DockerComposeBranch == "" && spec.DockerComposeRepo != "" {
		spec.DockerComposeBranch = "main"
	}
	if len(spec.DockerComposeUp) == 0 && spec.DockerComposeRepo != "" {
		spec.DockerComposeUp = []string{"docker compose up --remove-orphans"}
	}
	if len(spec.DockerComposeDown) == 0 && spec.DockerComposeRepo != "" {
		spec.DockerComposeDown = []string{"docker compose down"}
	}
	return spec
}

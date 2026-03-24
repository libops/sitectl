package plugin

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"

	corecomponent "github.com/libops/sitectl/pkg/component"
	"github.com/libops/sitectl/pkg/config"
	"github.com/libops/sitectl/pkg/helpers"
	"github.com/spf13/cobra"
	yaml "gopkg.in/yaml.v3"
)

type CheckoutSource string

const (
	CheckoutSourceTemplate CheckoutSource = "template"
	CheckoutSourceExisting CheckoutSource = "existing"
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

type ComposeCreateRequest struct {
	ContextName        string
	TargetType         config.ContextType
	CheckoutSource     CheckoutSource
	Path               string
	TemplateRepo       string
	TemplateBranch     string
	Site               string
	Environment        string
	ProjectName        string
	ComposeProjectName string
	ComposeNetwork     string
	DockerSocket       string
	SSHHostname        string
	SSHUser            string
	SSHPort            uint
	SSHKeyPath         string
	DrupalRootfs       string
	SetDefaultContext  bool
	SetupOnly          bool
	Decisions          map[string]corecomponent.ReviewDecision
}

type ComposeCreateContextOptions struct {
	DefaultName         string
	DefaultSite         string
	DefaultPlugin       string
	DefaultProjectDir   string
	DefaultProjectName  string
	DefaultEnvironment  string
	DefaultDockerSocket string
	DefaultDrupalRootfs string
	DrupalContainerRoot string
	ConfirmOverwrite    bool
	Input               config.InputFunc
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
	if !isDiscoveryMetadataInvocation() {
		runner.BindFlags(cmd)
	}
	s.RegisterCreate(spec, cmd)
}

func (s *SDK) RegisterComponentDefinition(def corecomponent.Definition) {
	if s == nil || strings.TrimSpace(def.Name) == "" {
		return
	}
	s.componentDefs = append(s.componentDefs, def)
}

func (s *SDK) RegisterComponentDefinitions(defs ...corecomponent.Definition) {
	for _, def := range defs {
		s.RegisterComponentDefinition(def)
	}
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

func (s *SDK) LocalComponentDefinitions() []corecomponent.Definition {
	if s == nil {
		return nil
	}
	out := make([]corecomponent.Definition, len(s.componentDefs))
	copy(out, s.componentDefs)
	return out
}

func (s *SDK) CreateComponentDefinitions() ([]corecomponent.Definition, error) {
	defs := s.LocalComponentDefinitions()
	for _, include := range s.Metadata.Includes {
		output, err := s.InvokeIncludedPluginCommand(include, []string{"__create", "component-definitions"}, CommandExecOptions{Capture: true})
		if err != nil {
			return nil, err
		}
		trimmed := strings.TrimSpace(output)
		if trimmed == "" {
			continue
		}
		var includeDefs []corecomponent.Definition
		if err := yaml.Unmarshal([]byte(trimmed), &includeDefs); err != nil {
			return nil, fmt.Errorf("parse create component definitions from plugin %q: %w", include, err)
		}
		defs = append(defs, includeDefs...)
	}
	return defs, nil
}

func (s *SDK) BindComposeCreateFlags(cmd *cobra.Command, spec CreateSpec, drupalRootfs *string, defaultDrupalRootfs string) error {
	if cmd == nil {
		return fmt.Errorf("create command is nil")
	}
	cmd.Flags().String("path", "", "Directory where the stack will be checked out.")
	cmd.Flags().String("project-dir", "", "Directory where the stack exists or will be created.")
	cmd.Flags().String("type", "", "Target machine for this stack: local or remote.")
	cmd.Flags().String("checkout-source", "", "How to source the project checkout: template or existing.")
	cmd.Flags().String("template-repo", spec.DockerComposeRepo, "Git repository to clone as the Docker Compose stack.")
	cmd.Flags().String("template-branch", normalizeCreateSpec(spec).DockerComposeBranch, "Branch or ref to clone from the template repository.")
	cmd.Flags().Bool("default-context", false, "Set the new context as the default sitectl context.")
	cmd.Flags().Bool("setup-only", false, "Clone and configure the checkout but do not start the stack.")
	cmd.Flags().String("ssh-hostname", "", "SSH hostname for a remote target.")
	cmd.Flags().Uint("ssh-port", 0, "SSH port for a remote target.")
	cmd.Flags().String("ssh-user", "", "SSH user for a remote target.")
	cmd.Flags().String("ssh-key", "", "Path to the SSH private key for a remote target.")
	cmd.Flags().String("site", "", "Logical site name this stack belongs to.")
	cmd.Flags().String("environment", "", "Environment name for the stack, such as local, dev, staging, or prod.")
	cmd.Flags().String("project-name", "", "Logical project name for this stack.")
	cmd.Flags().String("compose-project-name", "", "Docker Compose project name for this stack.")
	cmd.Flags().String("compose-network", "", "Primary Docker Compose network name for this stack.")
	cmd.Flags().String("docker-socket", "", "Docker socket path for the target machine.")
	defs, err := s.CreateComponentDefinitions()
	if err != nil {
		return err
	}
	options := make([]corecomponent.CreateOption, 0, len(defs))
	for _, def := range defs {
		options = append(options, def.CreateOption())
	}
	corecomponent.AddCreateFlags(cmd, options...)
	if drupalRootfs != nil {
		corecomponent.AddDrupalRootfsFlag(cmd, drupalRootfs, defaultDrupalRootfs)
	}
	return nil
}

func (s *SDK) ResolveComposeCreateRequest(cmd *cobra.Command, input config.InputFunc, drupalRootfs, defaultPath, defaultRepo, defaultBranch string) (ComposeCreateRequest, error) {
	if cmd == nil {
		return ComposeCreateRequest{}, fmt.Errorf("create command is nil")
	}
	if input == nil {
		input = config.GetInput
	}

	contextName := ""
	if flag := cmd.Flags().Lookup("context"); flag != nil && cmd.Flags().Changed("context") {
		value, err := cmd.Flags().GetString("context")
		if err != nil {
			return ComposeCreateRequest{}, fmt.Errorf("get context flag: %w", err)
		}
		contextName = strings.TrimSpace(value)
	}

	targetType, err := resolveCreateTargetType(cmd, input)
	if err != nil {
		return ComposeCreateRequest{}, err
	}
	checkoutSource, err := resolveCheckoutSource(cmd, input, targetType)
	if err != nil {
		return ComposeCreateRequest{}, err
	}
	pathValue, err := resolveCreateProjectDir(cmd, defaultPath)
	if err != nil {
		return ComposeCreateRequest{}, err
	}
	templateRepo, err := cmd.Flags().GetString("template-repo")
	if err != nil {
		return ComposeCreateRequest{}, fmt.Errorf("get template-repo flag: %w", err)
	}
	if strings.TrimSpace(templateRepo) == "" {
		templateRepo = defaultRepo
	}
	templateBranch, err := cmd.Flags().GetString("template-branch")
	if err != nil {
		return ComposeCreateRequest{}, fmt.Errorf("get template-branch flag: %w", err)
	}
	if strings.TrimSpace(templateBranch) == "" {
		templateBranch = defaultBranch
	}
	setDefaultContext, err := cmd.Flags().GetBool("default-context")
	if err != nil {
		return ComposeCreateRequest{}, fmt.Errorf("get default-context flag: %w", err)
	}
	setupOnly, err := cmd.Flags().GetBool("setup-only")
	if err != nil {
		return ComposeCreateRequest{}, fmt.Errorf("get setup-only flag: %w", err)
	}
	request := ComposeCreateRequest{
		ContextName:       contextName,
		TargetType:        targetType,
		CheckoutSource:    checkoutSource,
		Path:              strings.TrimSpace(pathValue),
		TemplateRepo:      strings.TrimSpace(templateRepo),
		TemplateBranch:    strings.TrimSpace(templateBranch),
		DrupalRootfs:      strings.TrimSpace(drupalRootfs),
		SetDefaultContext: setDefaultContext,
		SetupOnly:         setupOnly,
	}
	request.Site, _ = cmd.Flags().GetString("site")
	request.Site = strings.TrimSpace(request.Site)
	request.Environment, _ = cmd.Flags().GetString("environment")
	request.Environment = strings.TrimSpace(request.Environment)
	request.ProjectName, _ = cmd.Flags().GetString("project-name")
	request.ProjectName = strings.TrimSpace(request.ProjectName)
	request.ComposeProjectName, _ = cmd.Flags().GetString("compose-project-name")
	request.ComposeProjectName = strings.TrimSpace(request.ComposeProjectName)
	request.ComposeNetwork, _ = cmd.Flags().GetString("compose-network")
	request.ComposeNetwork = strings.TrimSpace(request.ComposeNetwork)
	request.DockerSocket, _ = cmd.Flags().GetString("docker-socket")
	request.DockerSocket = strings.TrimSpace(request.DockerSocket)
	request.SSHHostname, _ = cmd.Flags().GetString("ssh-hostname")
	request.SSHHostname = strings.TrimSpace(request.SSHHostname)
	request.SSHUser, _ = cmd.Flags().GetString("ssh-user")
	request.SSHUser = strings.TrimSpace(request.SSHUser)
	request.SSHPort, _ = cmd.Flags().GetUint("ssh-port")
	request.SSHKeyPath, _ = cmd.Flags().GetString("ssh-key")
	request.SSHKeyPath = strings.TrimSpace(request.SSHKeyPath)

	if request.TargetType == config.ContextRemote {
		if err := populateRemoteCreateRequest(&request, input); err != nil {
			return ComposeCreateRequest{}, err
		}
	}

	defs, err := s.CreateComponentDefinitions()
	if err != nil {
		return ComposeCreateRequest{}, err
	}
	options := make([]corecomponent.CreateOption, 0, len(defs))
	for _, def := range defs {
		options = append(options, def.CreateOption())
	}
	decisions, err := corecomponent.ResolveCreateDecisions(cmd, componentInput(input), options...)
	if err != nil {
		return ComposeCreateRequest{}, err
	}
	request.Decisions = decisions
	return request, nil
}

func (s *SDK) EnsureComposeCreateContext(req ComposeCreateRequest, opts ComposeCreateContextOptions) (*config.Context, error) {
	if s == nil {
		return nil, fmt.Errorf("plugin sdk is not initialized")
	}
	input := opts.Input
	if input == nil {
		input = config.GetInput
	}

	defaultDir := helpers.FirstNonEmpty(strings.TrimSpace(req.Path), strings.TrimSpace(opts.DefaultProjectDir), ".")
	defaultName := helpers.FirstNonEmpty(strings.TrimSpace(req.ContextName), strings.TrimSpace(opts.DefaultName), filepath.Base(defaultDir))
	defaultSite := helpers.FirstNonEmpty(strings.TrimSpace(req.Site), strings.TrimSpace(opts.DefaultSite), filepath.Base(defaultDir))
	defaultPlugin := helpers.FirstNonEmpty(strings.TrimSpace(opts.DefaultPlugin), s.Metadata.Name, "core")
	defaultProjectName := helpers.FirstNonEmpty(strings.TrimSpace(req.ProjectName), strings.TrimSpace(opts.DefaultProjectName), filepath.Base(defaultDir), "docker-compose")
	defaultEnvironment := helpers.FirstNonEmpty(strings.TrimSpace(req.Environment), strings.TrimSpace(opts.DefaultEnvironment))
	if req.TargetType == config.ContextLocal && defaultEnvironment == "" {
		defaultEnvironment = "local"
	}
	defaultDockerSocket := helpers.FirstNonEmpty(strings.TrimSpace(req.DockerSocket), strings.TrimSpace(opts.DefaultDockerSocket), "/var/run/docker.sock")

	if req.TargetType == config.ContextRemote {
		return promptAndSaveRemoteContext(ComposeRemoteContextOptions{
			ContextName:         req.ContextName,
			DefaultName:         defaultName,
			Site:                req.Site,
			DefaultSite:         defaultSite,
			Plugin:              defaultPlugin,
			ProjectDir:          req.Path,
			DefaultProjectDir:   defaultDir,
			ProjectName:         req.ProjectName,
			DefaultProjectName:  defaultProjectName,
			Environment:         req.Environment,
			DefaultEnvironment:  helpers.FirstNonEmpty(defaultEnvironment, "remote"),
			ComposeProjectName:  req.ComposeProjectName,
			ComposeNetwork:      req.ComposeNetwork,
			DockerSocket:        defaultDockerSocket,
			SSHHostname:         req.SSHHostname,
			SSHUser:             req.SSHUser,
			SSHPort:             req.SSHPort,
			SSHKeyPath:          req.SSHKeyPath,
			SetDefault:          req.SetDefaultContext,
			ConfirmOverwrite:    opts.ConfirmOverwrite,
			Input:               input,
			DrupalRootfs:        helpers.FirstNonEmpty(req.DrupalRootfs, opts.DefaultDrupalRootfs),
			DrupalContainerRoot: opts.DrupalContainerRoot,
		})
	}

	localOpts := config.LocalContextCreateOptions{
		Name:                req.ContextName,
		DefaultName:         defaultName,
		Site:                req.Site,
		DefaultSite:         defaultSite,
		Plugin:              defaultPlugin,
		DefaultPlugin:       defaultPlugin,
		ProjectDir:          req.Path,
		DefaultProjectDir:   defaultDir,
		ProjectName:         req.ProjectName,
		DefaultProjectName:  defaultProjectName,
		ComposeProjectName:  req.ComposeProjectName,
		ComposeNetwork:      req.ComposeNetwork,
		Environment:         defaultEnvironment,
		DockerSocket:        defaultDockerSocket,
		DrupalRootfs:        helpers.FirstNonEmpty(req.DrupalRootfs, opts.DefaultDrupalRootfs),
		DrupalContainerRoot: opts.DrupalContainerRoot,
		SetDefault:          req.SetDefaultContext,
		ConfirmOverwrite:    opts.ConfirmOverwrite,
		Input:               input,
	}
	if req.CheckoutSource == CheckoutSourceExisting {
		localOpts.ProjectDirValidator = config.ValidateExistingComposeProjectDir
	}
	return config.PromptAndSaveLocalContext(localOpts)
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
	componentDefinitionsCmd := &cobra.Command{
		Use:    "component-definitions",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			defs, err := s.CreateComponentDefinitions()
			if err != nil {
				return err
			}
			data, err := yaml.Marshal(defs)
			if err != nil {
				return fmt.Errorf("marshal create component definitions: %w", err)
			}
			_, err = cmd.OutOrStdout().Write(data)
			return err
		},
	}
	root.AddCommand(listCmd)
	root.AddCommand(componentDefinitionsCmd)
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

type ComposeRemoteContextOptions struct {
	ContextName         string
	DefaultName         string
	Site                string
	DefaultSite         string
	Plugin              string
	ProjectDir          string
	DefaultProjectDir   string
	ProjectName         string
	DefaultProjectName  string
	Environment         string
	DefaultEnvironment  string
	ComposeProjectName  string
	ComposeNetwork      string
	DockerSocket        string
	SSHHostname         string
	SSHUser             string
	SSHPort             uint
	SSHKeyPath          string
	SetDefault          bool
	ConfirmOverwrite    bool
	Input               config.InputFunc
	DrupalRootfs        string
	DrupalContainerRoot string
}

func promptAndSaveRemoteContext(opts ComposeRemoteContextOptions) (*config.Context, error) {
	input := opts.Input
	if input == nil {
		input = config.GetInput
	}

	name, err := resolveCreateContextName(opts.ContextName, opts.DefaultName, input)
	if err != nil {
		return nil, err
	}
	existing, err := config.GetContext(name)
	if err != nil && !strings.Contains(err.Error(), config.ErrContextNotFound.Error()) {
		return nil, err
	}
	if err == nil && existing.Name != "" && opts.ConfirmOverwrite {
		overwrite, promptErr := input("The context already exists. Do you want to overwrite it? [y/N]: ")
		if promptErr != nil {
			return nil, promptErr
		}
		if !isAffirmativeCreateAnswer(overwrite) {
			return nil, fmt.Errorf("context creation cancelled")
		}
	}

	projectDir, err := resolveRequiredCreateValue(input, "Project directory", helpers.FirstNonEmpty(strings.TrimSpace(opts.ProjectDir), strings.TrimSpace(opts.DefaultProjectDir)), strings.Split(corecomponent.RenderSection("Project directory", "Enter the full directory path where this stack exists or should be managed on the remote host."), "\n"))
	if err != nil {
		return nil, err
	}
	site := strings.TrimSpace(opts.Site)
	if site == "" {
		site, err = resolveRequiredCreateValue(input, "Site name", helpers.FirstNonEmpty(strings.TrimSpace(opts.DefaultSite), filepath.Base(projectDir)), strings.Split(corecomponent.RenderSection("Site name", "Enter the logical site name this context belongs to."), "\n"))
		if err != nil {
			return nil, err
		}
	}
	projectName := strings.TrimSpace(opts.ProjectName)
	if projectName == "" {
		projectName, err = resolveRequiredCreateValue(input, "Project name", helpers.FirstNonEmpty(strings.TrimSpace(opts.DefaultProjectName), filepath.Base(projectDir), "docker-compose"), strings.Split(corecomponent.RenderSection("Project name", "Enter the logical project name for this stack."), "\n"))
		if err != nil {
			return nil, err
		}
	}
	environment := strings.TrimSpace(opts.Environment)
	if environment == "" {
		environment, err = resolveRequiredCreateValue(input, "Environment", helpers.FirstNonEmpty(strings.TrimSpace(opts.DefaultEnvironment), "remote"), strings.Split(corecomponent.RenderSection("Environment", "Enter the environment name for this remote stack, such as dev, staging, or prod."), "\n"))
		if err != nil {
			return nil, err
		}
	}
	composeProjectName := helpers.FirstNonEmpty(strings.TrimSpace(opts.ComposeProjectName), projectName)
	composeNetwork := helpers.FirstNonEmpty(strings.TrimSpace(opts.ComposeNetwork), composeProjectName+"_default")
	hostname, err := resolveRequiredCreateValue(input, "SSH hostname", strings.TrimSpace(opts.SSHHostname), strings.Split(corecomponent.RenderSection("Remote SSH connection", "Enter the SSH connection details for the remote machine that hosts this stack."), "\n"))
	if err != nil {
		return nil, err
	}
	currentUser := ""
	if u, userErr := user.Current(); userErr == nil {
		currentUser = u.Username
	}
	sshUser, err := resolveRequiredCreateValue(input, "SSH user", helpers.FirstNonEmpty(strings.TrimSpace(opts.SSHUser), currentUser, "root"), nil)
	if err != nil {
		return nil, err
	}
	sshPort, err := resolveRequiredCreateUint(input, "SSH port", defaultCreateSSHPort(opts.SSHPort), nil)
	if err != nil {
		return nil, err
	}
	defaultKey := filepath.Join(os.Getenv("HOME"), ".ssh", "id_rsa")
	sshKeyPath, err := resolveRequiredCreateValue(input, "Path to SSH private key", helpers.FirstNonEmpty(strings.TrimSpace(opts.SSHKeyPath), defaultKey), nil)
	if err != nil {
		return nil, err
	}
	dockerSocket := helpers.FirstNonEmpty(strings.TrimSpace(opts.DockerSocket), "/var/run/docker.sock")

	ctx := &config.Context{
		Name:                name,
		Site:                site,
		Plugin:              helpers.FirstNonEmpty(strings.TrimSpace(opts.Plugin), "core"),
		DockerHostType:      config.ContextRemote,
		Environment:         environment,
		DockerSocket:        dockerSocket,
		ProjectName:         projectName,
		ComposeProjectName:  composeProjectName,
		ComposeNetwork:      composeNetwork,
		ProjectDir:          projectDir,
		DrupalRootfs:        strings.TrimSpace(opts.DrupalRootfs),
		DrupalContainerRoot: strings.TrimSpace(opts.DrupalContainerRoot),
		SSHHostname:         hostname,
		SSHUser:             sshUser,
		SSHPort:             sshPort,
		SSHKeyPath:          sshKeyPath,
	}
	if err := config.SaveContext(ctx, opts.SetDefault); err != nil {
		return nil, err
	}
	return ctx, nil
}

func resolveCreateTargetType(cmd *cobra.Command, input config.InputFunc) (config.ContextType, error) {
	value, err := cmd.Flags().GetString("type")
	if err != nil {
		return "", fmt.Errorf("get type flag: %w", err)
	}
	value = strings.TrimSpace(value)
	if value != "" {
		if value != string(config.ContextLocal) && value != string(config.ContextRemote) {
			return "", fmt.Errorf("unknown create target type %q", value)
		}
		return config.ContextType(value), nil
	}
	selected, err := corecomponent.PromptChoice(
		"create target",
		[]corecomponent.Choice{
			{Value: string(config.ContextLocal), Label: "local", Help: "Run this stack on your local machine."},
			{Value: string(config.ContextRemote), Label: "remote", Help: "Run this stack on a remote machine over SSH."},
		},
		string(config.ContextLocal),
		componentInput(input),
		strings.Split(corecomponent.RenderSection("Target machine", "Choose where this stack will run."), "\n")...,
	)
	if err != nil {
		return "", err
	}
	return config.ContextType(strings.TrimSpace(selected)), nil
}

func resolveCheckoutSource(cmd *cobra.Command, input config.InputFunc, targetType config.ContextType) (CheckoutSource, error) {
	value, err := cmd.Flags().GetString("checkout-source")
	if err != nil {
		return "", fmt.Errorf("get checkout-source flag: %w", err)
	}
	value = strings.TrimSpace(value)
	if value != "" {
		if value != string(CheckoutSourceTemplate) && value != string(CheckoutSourceExisting) {
			return "", fmt.Errorf("unknown checkout source %q", value)
		}
		return CheckoutSource(value), nil
	}
	defaultChoice := string(CheckoutSourceTemplate)
	if targetType == config.ContextRemote {
		defaultChoice = string(CheckoutSourceExisting)
	}
	selected, err := corecomponent.PromptChoice(
		"checkout source",
		[]corecomponent.Choice{
			{Value: string(CheckoutSourceTemplate), Label: "template", Help: "Clone the template repository as a fresh install."},
			{Value: string(CheckoutSourceExisting), Label: "existing", Help: "Use a repo or checkout that already exists."},
		},
		defaultChoice,
		componentInput(input),
		strings.Split(corecomponent.RenderSection("Project source", "Choose whether to create from the template repository or use an existing checkout."), "\n")...,
	)
	if err != nil {
		return "", err
	}
	return CheckoutSource(strings.TrimSpace(selected)), nil
}

func resolveCreateProjectDir(cmd *cobra.Command, defaultPath string) (string, error) {
	pathValue, err := cmd.Flags().GetString("project-dir")
	if err != nil {
		return "", fmt.Errorf("get project-dir flag: %w", err)
	}
	if strings.TrimSpace(pathValue) == "" {
		pathValue, err = cmd.Flags().GetString("path")
		if err != nil {
			return "", fmt.Errorf("get path flag: %w", err)
		}
	}
	if strings.TrimSpace(pathValue) == "" {
		pathValue = defaultPath
	}
	return strings.TrimSpace(pathValue), nil
}

func populateRemoteCreateRequest(req *ComposeCreateRequest, input config.InputFunc) error {
	if req == nil {
		return fmt.Errorf("create request is nil")
	}
	currentUser := ""
	if u, err := user.Current(); err == nil {
		currentUser = u.Username
	}
	defaultKey := filepath.Join(os.Getenv("HOME"), ".ssh", "id_rsa")
	var err error
	req.SSHHostname, err = resolveRequiredCreateValue(input, "SSH hostname", req.SSHHostname, strings.Split(corecomponent.RenderSection("Remote SSH connection", "Enter the SSH connection details for the remote machine that hosts this stack."), "\n"))
	if err != nil {
		return err
	}
	req.SSHUser, err = resolveRequiredCreateValue(input, "SSH user", helpers.FirstNonEmpty(req.SSHUser, currentUser, "root"), nil)
	if err != nil {
		return err
	}
	req.SSHPort, err = resolveRequiredCreateUint(input, "SSH port", defaultCreateSSHPort(req.SSHPort), nil)
	if err != nil {
		return err
	}
	req.SSHKeyPath, err = resolveRequiredCreateValue(input, "Path to SSH private key", helpers.FirstNonEmpty(req.SSHKeyPath, defaultKey), nil)
	if err != nil {
		return err
	}
	if strings.TrimSpace(req.DockerSocket) == "" {
		req.DockerSocket = "/var/run/docker.sock"
	}
	return nil
}

func resolveCreateContextName(explicitName, defaultName string, input config.InputFunc) (string, error) {
	if strings.TrimSpace(explicitName) != "" {
		return strings.TrimSpace(explicitName), nil
	}
	baseName := strings.TrimSpace(defaultName)
	if baseName == "" {
		return "", fmt.Errorf("context name cannot be empty")
	}
	exists, err := config.ContextExists(baseName)
	if err != nil {
		return "", err
	}
	if !exists {
		return baseName, nil
	}
	candidate, err := nextAvailableCreateContextName(baseName)
	if err != nil {
		return "", err
	}
	value, err := input(
		append(strings.Split(corecomponent.RenderSection("sitectl context name", "Enter the sitectl context name to save for this stack."), "\n"), "", corecomponent.RenderPromptLine(fmt.Sprintf("Context name [%s]: ", candidate)))...,
	)
	if err != nil {
		return "", err
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return candidate, nil
	}
	return value, nil
}

func nextAvailableCreateContextName(base string) (string, error) {
	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s-%d", base, i)
		exists, err := config.ContextExists(candidate)
		if err != nil {
			return "", err
		}
		if !exists {
			return candidate, nil
		}
	}
}

func resolveRequiredCreateValue(input config.InputFunc, label, defaultValue string, sections []string) (string, error) {
	prompt := fmt.Sprintf("%s: ", label)
	if strings.TrimSpace(defaultValue) != "" {
		prompt = fmt.Sprintf("%s [%s]: ", label, defaultValue)
	}
	question := append([]string{}, sections...)
	question = append(question, "", corecomponent.RenderPromptLine(prompt))
	value, err := input(question...)
	if err != nil {
		return "", err
	}
	value = strings.TrimSpace(value)
	if value == "" {
		value = strings.TrimSpace(defaultValue)
	}
	if value == "" {
		return "", fmt.Errorf("%s cannot be empty", strings.ToLower(label))
	}
	return value, nil
}

func resolveRequiredCreateUint(input config.InputFunc, label string, defaultValue uint, sections []string) (uint, error) {
	question := append([]string{}, sections...)
	question = append(question, "", corecomponent.RenderPromptLine(fmt.Sprintf("%s [%d]: ", label, defaultValue)))
	value, err := input(question...)
	if err != nil {
		return 0, err
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return defaultValue, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("invalid %s %q", strings.ToLower(label), value)
	}
	return uint(parsed), nil
}

func defaultCreateSSHPort(port uint) uint {
	if port != 0 {
		return port
	}
	return 22
}

func componentInput(input config.InputFunc) corecomponent.InputFunc {
	return func(question ...string) (string, error) {
		return input(question...)
	}
}

func isAffirmativeCreateAnswer(value string) bool {
	value = strings.TrimSpace(strings.ToLower(value))
	return value == "y" || value == "yes"
}

func isDiscoveryMetadataInvocation() bool {
	return len(os.Args) > 1 && os.Args[1] == "__plugin-metadata"
}

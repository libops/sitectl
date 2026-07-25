package plugin

import (
	"encoding/json"
	"errors"
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
	Name                 string             `json:"name" yaml:"name"`
	Plugin               string             `json:"plugin,omitempty" yaml:"plugin,omitempty"`
	Description          string             `json:"description,omitempty" yaml:"description,omitempty"`
	Default              bool               `json:"default,omitempty" yaml:"default,omitempty"`
	MinCPUCores          float64            `json:"min_cpu_cores,omitempty" yaml:"min_cpu_cores,omitempty"`
	MinMemory            string             `json:"min_memory,omitempty" yaml:"min_memory,omitempty"`
	MinDiskSpace         string             `json:"min_disk_space,omitempty" yaml:"min_disk_space,omitempty"`
	DockerComposeRepo    string             `json:"docker_compose_repo,omitempty" yaml:"docker_compose_repo,omitempty"`
	DockerComposeBranch  string             `json:"docker_compose_branch,omitempty" yaml:"docker_compose_branch,omitempty"`
	DockerComposeBuild   []string           `json:"docker_compose_build,omitempty" yaml:"docker_compose_build,omitempty"`
	DockerComposeInit    []string           `json:"docker_compose_init,omitempty" yaml:"docker_compose_init,omitempty"`
	InitArtifacts        []InitArtifact     `json:"init_artifacts,omitempty" yaml:"init_artifacts,omitempty"`
	InitVolumes          []InitVolume       `json:"init_volumes,omitempty" yaml:"init_volumes,omitempty"`
	Images               []ComposeImageSpec `json:"images,omitempty" yaml:"images,omitempty"`
	DockerComposeUp      []string           `json:"docker_compose_up,omitempty" yaml:"docker_compose_up,omitempty"`
	DockerComposeDown    []string           `json:"docker_compose_down,omitempty" yaml:"docker_compose_down,omitempty"`
	DockerComposeRollout []string           `json:"docker_compose_rollout,omitempty" yaml:"docker_compose_rollout,omitempty"`
}

type InitArtifact struct {
	Name      string `json:"name,omitempty" yaml:"name,omitempty"`
	Path      string `json:"path" yaml:"path"`
	ValueFrom string `json:"value_from,omitempty" yaml:"value_from,omitempty"`
}

type InitVolume struct {
	Name string `json:"name" yaml:"name"`
}

const InitArtifactValueFromHostUID = "HostUID"

type ImagePullPolicy string

const (
	ImagePullPolicyAlways       ImagePullPolicy = "Always"
	ImagePullPolicyNever        ImagePullPolicy = "Never"
	ImagePullPolicyIfNotPresent ImagePullPolicy = "IfNotPresent"
)

type BuildPolicy string

const (
	BuildPolicyAlways       BuildPolicy = "Always"
	BuildPolicyNever        BuildPolicy = "Never"
	BuildPolicyIfNotPresent BuildPolicy = "IfNotPresent"
)

type ComposeImageSpec struct {
	Name            string          `json:"name,omitempty" yaml:"name,omitempty"`
	Service         string          `json:"service" yaml:"service"`
	Image           string          `json:"image" yaml:"image"`
	ImagePullPolicy ImagePullPolicy `json:"image_pull_policy,omitempty" yaml:"image_pull_policy,omitempty"`
	BuildPolicy     BuildPolicy     `json:"build_policy,omitempty" yaml:"build_policy,omitempty"`
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
	ContextName    string
	TargetType     config.ContextType
	CheckoutSource CheckoutSource
	Path           string
	TemplateRepo   string
	TemplateBranch string
	Site           string
	Environment    string
	// ProjectName is retained for source compatibility with older plugins.
	// Deprecated: use ComposeProjectName.
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
	Yolo               bool
	ImageOverrides     ComposeImageOverrides
	Decisions          map[string]corecomponent.ReviewDecision
}

type ComposeCreateContextOptions struct {
	DefaultName                   string
	DefaultSite                   string
	DefaultPlugin                 string
	DefaultProjectDir             string
	DefaultProjectName            string
	DefaultEnvironment            string
	DefaultDockerSocket           string
	DefaultDatabaseService        string
	DefaultDatabaseUser           string
	DefaultDatabasePasswordSecret string
	DefaultDatabaseName           string
	DefaultDrupalRootfs           string
	DrupalContainerRoot           string
	ConfirmOverwrite              bool
	Input                         config.InputFunc
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
	defs := appendUniqueComponentDefinitions(nil, s.LocalComponentDefinitions())
	for _, include := range s.Metadata.Includes {
		resp, err := s.InvokeIncludedPluginRPC(include, NewRPCRequest(MethodCreateComponentDefinitions), CommandExecOptions{})
		if err != nil {
			return nil, err
		}
		if len(resp.Result) == 0 {
			continue
		}
		var includeDefs []corecomponent.Definition
		if err := json.Unmarshal(resp.Result, &includeDefs); err != nil {
			return nil, fmt.Errorf("parse create component definitions from plugin %q: %w", include, err)
		}
		defs = appendUniqueComponentDefinitions(defs, includeDefs)
	}
	return defs, nil
}

func appendUniqueComponentDefinitions(defs, incoming []corecomponent.Definition) []corecomponent.Definition {
	if len(incoming) == 0 {
		return defs
	}

	seen := make(map[string]struct{}, len(defs)+len(incoming))
	for _, def := range defs {
		if def.Name != "" {
			seen[def.Name] = struct{}{}
		}
	}

	for _, def := range incoming {
		if def.Name != "" {
			if _, ok := seen[def.Name]; ok {
				continue
			}
			seen[def.Name] = struct{}{}
		}
		defs = append(defs, def)
	}
	return defs
}

func (s *SDK) BindComposeCreateFlags(cmd *cobra.Command, spec CreateSpec, drupalRootfs *string, defaultDrupalRootfs string) error {
	if cmd == nil {
		return fmt.Errorf("create command is nil")
	}
	cmd.Flags().String("path", "", "Directory where the stack will be checked out.")
	cmd.Flags().String("project-dir", "", "Directory where the stack exists or will be created.")
	cmd.Flags().String("type", "", "Target machine for this stack: local or remote.")
	cmd.Flags().String("checkout-source", "", "How to source the project checkout: template or existing.")
	if cmd.Flags().Lookup("context") == nil {
		cmd.Flags().String("context", "", "sitectl context name to save for this stack.")
	}
	cmd.Flags().String("template-repo", spec.DockerComposeRepo, "Git repository to clone as the Docker Compose stack.")
	cmd.Flags().String("template-branch", normalizeCreateSpec(spec).DockerComposeBranch, "Branch or ref to clone from the template repository.")
	cmd.Flags().Bool("default-context", false, "Set the new context as the default sitectl context.")
	cmd.Flags().Bool("setup-only", false, "Clone and configure the checkout but do not start the stack.")
	cmd.Flags().Bool("yolo", false, "Accept resolved defaults without reviewing context, component, or host-change decisions.")
	cmd.Flags().StringArray("tag", []string{}, "Set a LibOps image tag for a known Compose service as SERVICE=TAG; may be passed more than once.")
	cmd.Flags().StringArray("image", []string{}, "Override a non-buildable Compose service image as SERVICE=IMAGE; may be passed more than once.")
	cmd.Flags().StringArray("build-arg", []string{}, "Override a Compose service build arg as SERVICE.ARG=VALUE; may be passed more than once.")
	cmd.Flags().String("ssh-hostname", "", "SSH hostname for a remote target.")
	cmd.Flags().Uint("ssh-port", 0, "SSH port for a remote target.")
	cmd.Flags().String("ssh-user", "", "SSH user for a remote target.")
	cmd.Flags().String("ssh-key", "", "Path to the SSH private key for a remote target.")
	cmd.Flags().String("site", "", "Logical site name this stack belongs to.")
	cmd.Flags().String("environment", "", "Environment name for the stack, such as local, dev, staging, or prod.")
	cmd.Flags().String("project-name", "", "Deprecated compatibility alias for --compose-project-name.")
	if err := cmd.Flags().MarkDeprecated("project-name", "use --site and --compose-project-name instead"); err != nil {
		return fmt.Errorf("deprecate project-name flag: %w", err)
	}
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

func (s *SDK) ResolveComposeCreateRequest(cmd *cobra.Command, input config.InputFunc, pluginName, drupalRootfs, defaultPath, defaultRepo, defaultBranch string) (ComposeCreateRequest, error) {
	if cmd == nil {
		return ComposeCreateRequest{}, fmt.Errorf("create command is nil")
	}
	if input == nil {
		input = config.GetInput
	}

	yolo, err := cmd.Flags().GetBool("yolo")
	if err != nil {
		return ComposeCreateRequest{}, fmt.Errorf("get yolo flag: %w", err)
	}

	contextName := ""
	if flag := cmd.Flags().Lookup("context"); flag != nil && cmd.Flags().Changed("context") {
		value, err := cmd.Flags().GetString("context")
		if err != nil {
			return ComposeCreateRequest{}, fmt.Errorf("get context flag: %w", err)
		}
		contextName = strings.TrimSpace(value)
	}
	existing := config.Context{}
	if contextName != "" {
		stored, getErr := config.GetContext(contextName)
		if getErr == nil {
			existing = stored
		} else if !errors.Is(getErr, config.ErrContextNotFound) {
			return ComposeCreateRequest{}, getErr
		}
	}

	pathValue, err := resolveCreateProjectDir(cmd, input, defaultPath, existing.ProjectDir, yolo)
	if err != nil {
		return ComposeCreateRequest{}, err
	}
	contextName, err = reviewCreateContextName(contextName, filepath.Base(pathValue), input, yolo)
	if err != nil {
		return ComposeCreateRequest{}, err
	}

	targetType, err := resolveCreateTargetType(cmd, input, existing.DockerHostType, yolo)
	if err != nil {
		return ComposeCreateRequest{}, err
	}
	checkoutSource, err := resolveCheckoutSource(cmd, input, targetType, yolo)
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
	if checkoutSource == CheckoutSourceTemplate {
		templateRepo, err = resolveCreateStringDecision(cmd, input, "template-repo", "Template repository", "This Git repository supplies every initial project file for the new checkout.", templateRepo, yolo)
		if err != nil {
			return ComposeCreateRequest{}, err
		}
	}
	templateBranch, err := cmd.Flags().GetString("template-branch")
	if err != nil {
		return ComposeCreateRequest{}, fmt.Errorf("get template-branch flag: %w", err)
	}
	if strings.TrimSpace(templateBranch) == "" {
		templateBranch = defaultBranch
	}
	if checkoutSource == CheckoutSourceTemplate {
		templateBranch, err = resolveCreateStringDecision(cmd, input, "template-branch", "Template version", "This branch or ref selects the exact template revision used for the new checkout.", templateBranch, yolo)
		if err != nil {
			return ComposeCreateRequest{}, err
		}
	}
	cfg, err := config.Load()
	if err != nil {
		return ComposeCreateRequest{}, err
	}
	firstContext := strings.TrimSpace(cfg.CurrentContext) == ""
	defaultContextFallback := firstContext || (contextName != "" && strings.EqualFold(cfg.CurrentContext, contextName))
	defaultContextImplication := "Make this the context used when --context and directory discovery do not select another site. This changes which site later commands target by default."
	if firstContext {
		defaultContextImplication += " Because no fallback exists yet, the first saved context must become the default."
	}
	setDefaultContext, err := resolveCreateBoolDecision(cmd, input, "default-context", "Default context", defaultContextImplication, defaultContextFallback, yolo)
	if err != nil {
		return ComposeCreateRequest{}, err
	}
	if firstContext && !setDefaultContext {
		if yolo {
			setDefaultContext = true
		} else {
			return ComposeCreateRequest{}, fmt.Errorf("the first saved context must be the default context")
		}
	}
	setupOnly, err := resolveCreateBoolDecision(cmd, input, "setup-only", "Startup", "Choose whether creation should only prepare files or also start the Docker Compose stack.", false, yolo)
	if err != nil {
		return ComposeCreateRequest{}, err
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
		Yolo:              yolo,
	}
	request.Site, err = resolveCreateStringDecision(cmd, input, "site", "Site identity", "Contexts with the same site value are treated as environments of one logical site.", helpers.FirstNonEmpty(existing.Site, filepath.Base(pathValue)), yolo)
	if err != nil {
		return ComposeCreateRequest{}, err
	}
	defaultEnvironment := helpers.FirstNonEmpty(existing.Environment, "local")
	if targetType == config.ContextRemote {
		defaultEnvironment = helpers.FirstNonEmpty(existing.Environment, "remote")
	}
	request.Environment, err = resolveCreateStringDecision(cmd, input, "environment", "Environment", "This labels where the site runs (for example local, staging, or prod) and distinguishes contexts for the same site.", defaultEnvironment, yolo)
	if err != nil {
		return ComposeCreateRequest{}, err
	}
	legacyProjectName := ""
	if flag := cmd.Flags().Lookup("project-name"); flag != nil && flag.Changed {
		legacyProjectName, err = cmd.Flags().GetString("project-name")
		if err != nil {
			return ComposeCreateRequest{}, fmt.Errorf("get project-name flag: %w", err)
		}
		legacyProjectName = strings.TrimSpace(legacyProjectName)
	}
	request.ProjectName = legacyProjectName
	request.ComposeProjectName, err = resolveCreateStringDecision(cmd, input, "compose-project-name", "Compose project identity", "Docker Compose uses this name to label containers, volumes, and networks. Changing it makes Compose treat the stack as a different project.", helpers.FirstNonEmpty(legacyProjectName, existing.ComposeProjectName, filepath.Base(pathValue)), yolo)
	if err != nil {
		return ComposeCreateRequest{}, err
	}
	request.ComposeNetwork, err = resolveCreateStringDecision(cmd, input, "compose-network", "Compose network", "sitectl uses this network to resolve and connect to services in the stack.", helpers.FirstNonEmpty(existing.ComposeNetwork, request.ComposeProjectName+"_default"), yolo)
	if err != nil {
		return ComposeCreateRequest{}, err
	}
	request.DockerSocket, err = resolveCreateStringDecision(cmd, input, "docker-socket", "Docker socket", "This Unix socket is where sitectl sends Docker API requests on the target machine.", helpers.FirstNonEmpty(existing.DockerSocket, "/var/run/docker.sock"), yolo)
	if err != nil {
		return ComposeCreateRequest{}, err
	}
	request.SSHHostname, _ = cmd.Flags().GetString("ssh-hostname")
	request.SSHHostname = strings.TrimSpace(request.SSHHostname)
	request.SSHUser, _ = cmd.Flags().GetString("ssh-user")
	request.SSHUser = strings.TrimSpace(request.SSHUser)
	request.SSHPort, _ = cmd.Flags().GetUint("ssh-port")
	request.SSHKeyPath, _ = cmd.Flags().GetString("ssh-key")
	request.SSHKeyPath = strings.TrimSpace(request.SSHKeyPath)
	request.ImageOverrides, err = resolveCreateImageOverrides(cmd, pluginName)
	if err != nil {
		return ComposeCreateRequest{}, err
	}

	if request.TargetType == config.ContextRemote {
		if err := populateRemoteCreateRequest(&request, existing, input, yolo); err != nil {
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
	options, restoreFlags, err := prepareCreateDecisionReview(cmd, options, yolo)
	if err != nil {
		return ComposeCreateRequest{}, err
	}
	defer restoreFlags()
	decisions, err := corecomponent.ResolveCreateDecisions(cmd, componentInput(input), options...)
	if err != nil {
		return ComposeCreateRequest{}, err
	}
	request.Decisions = decisions
	if !yolo {
		if err := confirmComposeCreateRequest(request, input); err != nil {
			return ComposeCreateRequest{}, err
		}
	}
	return request, nil
}

func confirmComposeCreateRequest(request ComposeCreateRequest, input config.InputFunc) error {
	defaultBehavior := "keep the current default context"
	if request.SetDefaultContext {
		defaultBehavior = "make this the default context"
	}
	startupBehavior := "start the Docker Compose stack after setup"
	if request.SetupOnly {
		startupBehavior = "prepare files without starting the stack"
	}
	summary := fmt.Sprintf(
		"Create context %q for %s on the %s target. Operate Compose project %q as site %q, environment %q; %s; %s.",
		request.ContextName,
		request.Path,
		request.TargetType,
		request.ComposeProjectName,
		request.Site,
		request.Environment,
		defaultBehavior,
		startupBehavior,
	)
	if request.CheckoutSource == CheckoutSourceTemplate {
		summary += fmt.Sprintf(" Clone %s at %s.", request.TemplateRepo, request.TemplateBranch)
	} else {
		summary += " Use the existing project checkout."
	}
	selected, err := corecomponent.PromptChoice(
		"create review",
		[]corecomponent.Choice{
			{Value: "yes", Label: "yes", Help: "Apply these reviewed context and stack-creation decisions."},
			{Value: "no", Label: "no", Help: "Leave the context configuration and project files unchanged."},
		},
		"yes",
		componentInput(input),
		strings.Split(corecomponent.RenderSection("Review create", summary), "\n")...,
	)
	if err != nil {
		return err
	}
	if selected != "yes" {
		return fmt.Errorf("stack creation cancelled")
	}
	return nil
}

func prepareCreateDecisionReview(cmd *cobra.Command, options []corecomponent.CreateOption, yolo bool) ([]corecomponent.CreateOption, func(), error) {
	prepared := append([]corecomponent.CreateOption{}, options...)
	restore := []func(){}
	for i := range prepared {
		prepared[i].FollowUps = append([]corecomponent.FollowUpSpec{}, prepared[i].FollowUps...)
		if yolo {
			prepared[i].PromptOnCreate = false
			for j := range prepared[i].FollowUps {
				prepared[i].FollowUps[j].PromptOnCreate = false
			}
			continue
		}
		flag := cmd.Flags().Lookup(prepared[i].Name)
		if flag != nil && flag.Changed && prepared[i].PromptOnCreate {
			value, err := cmd.Flags().GetString(prepared[i].Name)
			if err != nil {
				return nil, func() {}, fmt.Errorf("get %s flag: %w", prepared[i].Name, err)
			}
			disposition, err := corecomponent.ParseDisposition(value)
			if err != nil {
				return nil, func() {}, fmt.Errorf("invalid %s value %q: %w", prepared[i].Name, value, err)
			}
			disposition, err = corecomponent.ResolveAllowedDisposition(prepared[i].AllowedDispositions, disposition)
			if err != nil {
				return nil, func() {}, fmt.Errorf("invalid %s value %q: %w", prepared[i].Name, value, err)
			}
			prepared[i].DefaultDisposition = disposition
			flag.Changed = false
			restore = append(restore, func() { flag.Changed = true })
		}
		for j := range prepared[i].FollowUps {
			followUp := &prepared[i].FollowUps[j]
			if !followUp.PromptOnCreate {
				continue
			}
			flagName := corecomponent.FollowUpFlagName(prepared[i].Name, *followUp)
			followUpFlag := cmd.Flags().Lookup(flagName)
			if followUpFlag == nil || !followUpFlag.Changed {
				continue
			}
			switch {
			case followUp.BoolValue:
				value, getErr := cmd.Flags().GetBool(flagName)
				if getErr != nil {
					return nil, func() {}, fmt.Errorf("get %s flag: %w", flagName, getErr)
				}
				followUp.DefaultValue = corecomponent.FormatFollowUpBool(value)
			case followUp.MultiValue:
				value, getErr := cmd.Flags().GetStringArray(flagName)
				if getErr != nil {
					return nil, func() {}, fmt.Errorf("get %s flag: %w", flagName, getErr)
				}
				followUp.DefaultValue = corecomponent.JoinFollowUpValues(value)
			default:
				value, getErr := cmd.Flags().GetString(flagName)
				if getErr != nil {
					return nil, func() {}, fmt.Errorf("get %s flag: %w", flagName, getErr)
				}
				followUp.DefaultValue = strings.TrimSpace(value)
			}
			followUpFlag.Changed = false
			restore = append(restore, func() { followUpFlag.Changed = true })
		}
	}
	return prepared, func() {
		for _, restoreFlag := range restore {
			restoreFlag()
		}
	}, nil
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
	defaultProjectName := helpers.FirstNonEmpty(strings.TrimSpace(req.ComposeProjectName), strings.TrimSpace(req.ProjectName), strings.TrimSpace(opts.DefaultProjectName), filepath.Base(defaultDir), "docker-compose")
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
			DatabaseService:     opts.DefaultDatabaseService,
			DatabaseUser:        opts.DefaultDatabaseUser,
			DatabaseSecret:      opts.DefaultDatabasePasswordSecret,
			DatabaseName:        opts.DefaultDatabaseName,
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
		DatabaseService:     opts.DefaultDatabaseService,
		DatabaseUser:        opts.DefaultDatabaseUser,
		DatabaseSecret:      opts.DefaultDatabasePasswordSecret,
		DatabaseName:        opts.DefaultDatabaseName,
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
		Use:          "create",
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
	spec.InitArtifacts = normalizeInitArtifacts(spec.InitArtifacts)
	spec.InitVolumes = normalizeInitVolumes(spec.InitVolumes)
	spec.Images = normalizeComposeImageSpecs(spec.Images)
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

func normalizeInitArtifacts(values []InitArtifact) []InitArtifact {
	if len(values) == 0 {
		return nil
	}
	out := make([]InitArtifact, 0, len(values))
	for _, value := range values {
		value.Name = strings.TrimSpace(value.Name)
		value.Path = strings.TrimSpace(value.Path)
		value.ValueFrom = strings.TrimSpace(value.ValueFrom)
		if value.Path != "" {
			out = append(out, value)
		}
	}
	return out
}

func normalizeInitVolumes(values []InitVolume) []InitVolume {
	if len(values) == 0 {
		return nil
	}
	out := make([]InitVolume, 0, len(values))
	for _, value := range values {
		value.Name = strings.TrimSpace(value.Name)
		if value.Name != "" {
			out = append(out, value)
		}
	}
	return out
}

func normalizeComposeImageSpecs(values []ComposeImageSpec) []ComposeImageSpec {
	if len(values) == 0 {
		return nil
	}
	out := make([]ComposeImageSpec, 0, len(values))
	for _, value := range values {
		value.Name = strings.TrimSpace(value.Name)
		value.Service = strings.TrimSpace(value.Service)
		value.Image = strings.TrimSpace(value.Image)
		if value.ImagePullPolicy == "" {
			value.ImagePullPolicy = ImagePullPolicyIfNotPresent
		}
		if value.BuildPolicy == "" {
			value.BuildPolicy = BuildPolicyIfNotPresent
		}
		if value.Service != "" && value.Image != "" {
			out = append(out, value)
		}
	}
	return out
}

type ComposeRemoteContextOptions struct {
	ContextName       string
	DefaultName       string
	Site              string
	DefaultSite       string
	Plugin            string
	ProjectDir        string
	DefaultProjectDir string
	// ProjectName is retained for source compatibility with older plugins.
	// Deprecated: use ComposeProjectName.
	ProjectName         string
	DefaultProjectName  string
	Environment         string
	DefaultEnvironment  string
	ComposeProjectName  string
	ComposeNetwork      string
	DockerSocket        string
	DatabaseService     string
	DatabaseUser        string
	DatabaseSecret      string
	DatabaseName        string
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

	projectDir := helpers.FirstNonEmpty(strings.TrimSpace(opts.ProjectDir), strings.TrimSpace(opts.DefaultProjectDir))
	if projectDir == "" {
		projectDir, err = resolveRequiredCreateValue(input, "Project directory", "", strings.Split(corecomponent.RenderSection("Project directory", "Enter the full directory path where this stack exists or should be managed on the remote host."), "\n"))
		if err != nil {
			return nil, err
		}
	}
	site := helpers.FirstNonEmpty(strings.TrimSpace(opts.Site), strings.TrimSpace(opts.DefaultSite), filepath.Base(projectDir))
	environment := helpers.FirstNonEmpty(strings.TrimSpace(opts.Environment), strings.TrimSpace(opts.DefaultEnvironment), "remote")
	composeProjectName := helpers.FirstNonEmpty(strings.TrimSpace(opts.ComposeProjectName), strings.TrimSpace(opts.ProjectName), strings.TrimSpace(opts.DefaultProjectName), filepath.Base(projectDir), "docker-compose")
	composeNetwork := helpers.FirstNonEmpty(strings.TrimSpace(opts.ComposeNetwork), composeProjectName+"_default")
	hostname := strings.TrimSpace(opts.SSHHostname)
	if hostname == "" {
		hostname, err = resolveRequiredCreateValue(input, "SSH hostname", "", strings.Split(corecomponent.RenderSection("Remote SSH connection", "Enter the SSH connection details for the remote machine that hosts this stack."), "\n"))
		if err != nil {
			return nil, err
		}
	}
	currentUser := ""
	if u, userErr := user.Current(); userErr == nil {
		currentUser = u.Username
	}
	sshUser := strings.TrimSpace(opts.SSHUser)
	if sshUser == "" {
		sshUser, err = resolveRequiredCreateValue(input, "SSH user", helpers.FirstNonEmpty(currentUser, "root"), nil)
		if err != nil {
			return nil, err
		}
	}
	sshPort := opts.SSHPort
	if sshPort == 0 {
		sshPort, err = resolveRequiredCreateUint(input, "SSH port", defaultCreateSSHPort(sshPort), nil)
		if err != nil {
			return nil, err
		}
	}
	defaultKey := filepath.Join(os.Getenv("HOME"), ".ssh", "id_rsa")
	sshKeyPath := strings.TrimSpace(opts.SSHKeyPath)
	if sshKeyPath == "" {
		sshKeyPath, err = resolveRequiredCreateValue(input, "Path to SSH private key", defaultKey, nil)
		if err != nil {
			return nil, err
		}
	}
	dockerSocket := helpers.FirstNonEmpty(strings.TrimSpace(opts.DockerSocket), "/var/run/docker.sock")

	ctx := &config.Context{
		Name:                   name,
		Site:                   site,
		Plugin:                 helpers.FirstNonEmpty(strings.TrimSpace(opts.Plugin), "core"),
		DockerHostType:         config.ContextRemote,
		Environment:            environment,
		DockerSocket:           dockerSocket,
		ComposeProjectName:     composeProjectName,
		ComposeNetwork:         composeNetwork,
		ProjectDir:             projectDir,
		DatabaseService:        strings.TrimSpace(opts.DatabaseService),
		DatabaseUser:           strings.TrimSpace(opts.DatabaseUser),
		DatabasePasswordSecret: strings.TrimSpace(opts.DatabaseSecret),
		DatabaseName:           strings.TrimSpace(opts.DatabaseName),
		DrupalRootfs:           strings.TrimSpace(opts.DrupalRootfs),
		DrupalContainerRoot:    strings.TrimSpace(opts.DrupalContainerRoot),
		SSHHostname:            hostname,
		SSHUser:                sshUser,
		SSHPort:                sshPort,
		SSHKeyPath:             sshKeyPath,
	}
	if err := config.SaveContext(ctx, opts.SetDefault); err != nil {
		return nil, err
	}
	return ctx, nil
}

func resolveCreateTargetType(cmd *cobra.Command, input config.InputFunc, existing config.ContextType, yolo bool) (config.ContextType, error) {
	value, err := cmd.Flags().GetString("type")
	if err != nil {
		return "", fmt.Errorf("get type flag: %w", err)
	}
	value = strings.TrimSpace(value)
	defaultValue := value
	if defaultValue == "" {
		defaultValue = helpers.FirstNonEmpty(string(existing), string(config.ContextLocal))
	}
	if defaultValue != string(config.ContextLocal) && defaultValue != string(config.ContextRemote) {
		return "", fmt.Errorf("unknown create target type %q", defaultValue)
	}
	if yolo {
		return config.ContextType(defaultValue), nil
	}
	selected, err := corecomponent.PromptChoice(
		"create target",
		[]corecomponent.Choice{
			{Value: string(config.ContextLocal), Label: "local", Help: "Run this stack on your local machine."},
			{Value: string(config.ContextRemote), Label: "remote", Help: "Run this stack on a remote machine over SSH."},
		},
		defaultValue,
		componentInput(input),
		strings.Split(corecomponent.RenderSection("Target machine", "Choose where this stack will run."), "\n")...,
	)
	if err != nil {
		return "", err
	}
	return config.ContextType(strings.TrimSpace(selected)), nil
}

func resolveCheckoutSource(cmd *cobra.Command, input config.InputFunc, targetType config.ContextType, yolo bool) (CheckoutSource, error) {
	value, err := cmd.Flags().GetString("checkout-source")
	if err != nil {
		return "", fmt.Errorf("get checkout-source flag: %w", err)
	}
	value = strings.TrimSpace(value)
	defaultChoice := string(CheckoutSourceTemplate)
	if targetType == config.ContextRemote {
		defaultChoice = string(CheckoutSourceExisting)
	}
	if value != "" {
		defaultChoice = value
	}
	if defaultChoice != string(CheckoutSourceTemplate) && defaultChoice != string(CheckoutSourceExisting) {
		return "", fmt.Errorf("unknown checkout source %q", defaultChoice)
	}
	if yolo {
		return CheckoutSource(defaultChoice), nil
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

func resolveCreateProjectDir(cmd *cobra.Command, input config.InputFunc, defaultPath, existingPath string, yolo bool) (string, error) {
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
		pathValue = helpers.FirstNonEmpty(strings.TrimSpace(existingPath), strings.TrimSpace(defaultPath))
	}
	if strings.TrimSpace(pathValue) == "" {
		cwd, cwdErr := os.Getwd()
		if cwdErr != nil {
			return "", fmt.Errorf("resolve current directory: %w", cwdErr)
		}
		pathValue = cwd
	}
	if yolo {
		return strings.TrimSpace(pathValue), nil
	}
	return resolveRequiredCreateValue(input, "Project path", strings.TrimSpace(pathValue), strings.Split(corecomponent.RenderSection(
		"Working path",
		"sitectl derives context names, Compose identity, checkout behavior, and several later defaults from this directory. Enter a different path now, or press Ctrl-C, change directory, and run create again if these assumptions should be based somewhere else.",
	), "\n"))
}

func populateRemoteCreateRequest(req *ComposeCreateRequest, existing config.Context, input config.InputFunc, yolo bool) error {
	if req == nil {
		return fmt.Errorf("create request is nil")
	}
	currentUser := ""
	if u, err := user.Current(); err == nil {
		currentUser = u.Username
	}
	defaultKey := filepath.Join(os.Getenv("HOME"), ".ssh", "id_rsa")
	var err error
	if strings.TrimSpace(req.SSHHostname) == "" {
		req.SSHHostname = strings.TrimSpace(existing.SSHHostname)
	}
	if strings.TrimSpace(req.SSHHostname) == "" || !yolo {
		req.SSHHostname, err = resolveRequiredCreateValue(input, "SSH hostname", req.SSHHostname, strings.Split(corecomponent.RenderSection("Remote SSH connection", "This host receives Docker and filesystem operations over SSH. Changing it targets a different machine."), "\n"))
		if err != nil {
			return err
		}
	}
	if strings.TrimSpace(req.SSHUser) == "" {
		req.SSHUser = strings.TrimSpace(existing.SSHUser)
	}
	if strings.TrimSpace(req.SSHUser) == "" || !yolo {
		req.SSHUser, err = resolveRequiredCreateValue(input, "SSH user", helpers.FirstNonEmpty(req.SSHUser, currentUser, "root"), strings.Split(corecomponent.RenderSection("SSH user", "Commands and remote file changes run with this account's permissions."), "\n"))
		if err != nil {
			return err
		}
	}
	if req.SSHPort == 0 {
		req.SSHPort = existing.SSHPort
	}
	if req.SSHPort == 0 || !yolo {
		req.SSHPort, err = resolveRequiredCreateUint(input, "SSH port", defaultCreateSSHPort(req.SSHPort), strings.Split(corecomponent.RenderSection("SSH port", "This is the TCP port used to establish the remote transport."), "\n"))
		if err != nil {
			return err
		}
	}
	if strings.TrimSpace(req.SSHKeyPath) == "" {
		req.SSHKeyPath = strings.TrimSpace(existing.SSHKeyPath)
	}
	if strings.TrimSpace(req.SSHKeyPath) == "" || !yolo {
		req.SSHKeyPath, err = resolveRequiredCreateValue(input, "Path to SSH private key", helpers.FirstNonEmpty(req.SSHKeyPath, defaultKey), strings.Split(corecomponent.RenderSection("SSH identity", "This private key authenticates sitectl to the remote host; the key contents are never stored in the context."), "\n"))
		if err != nil {
			return err
		}
	}
	if strings.TrimSpace(req.DockerSocket) == "" {
		req.DockerSocket = "/var/run/docker.sock"
	}
	return nil
}

func resolveCreateStringDecision(cmd *cobra.Command, input config.InputFunc, flagName, title, implication, fallback string, yolo bool) (string, error) {
	value := ""
	if flag := cmd.Flags().Lookup(flagName); flag != nil && cmd.Flags().Changed(flagName) {
		var err error
		value, err = cmd.Flags().GetString(flagName)
		if err != nil {
			return "", fmt.Errorf("get %s flag: %w", flagName, err)
		}
	}
	defaultValue := helpers.FirstNonEmpty(strings.TrimSpace(value), strings.TrimSpace(fallback))
	if yolo {
		if defaultValue == "" {
			return "", fmt.Errorf("%s cannot be empty", strings.ReplaceAll(flagName, "-", " "))
		}
		return defaultValue, nil
	}
	return resolveRequiredCreateValue(input, strings.ReplaceAll(flagName, "-", " "), defaultValue, strings.Split(corecomponent.RenderSection(title, implication), "\n"))
}

func resolveCreateBoolDecision(cmd *cobra.Command, input config.InputFunc, flagName, title, implication string, fallback, yolo bool) (bool, error) {
	value := fallback
	if flag := cmd.Flags().Lookup(flagName); flag != nil && cmd.Flags().Changed(flagName) {
		var err error
		value, err = cmd.Flags().GetBool(flagName)
		if err != nil {
			return false, fmt.Errorf("get %s flag: %w", flagName, err)
		}
	}
	if yolo {
		return value, nil
	}
	defaultValue := "no"
	if value {
		defaultValue = "yes"
	}
	selected, err := corecomponent.PromptChoice(flagName, []corecomponent.Choice{
		{Value: "yes", Label: "yes", Help: "Enable this behavior."},
		{Value: "no", Label: "no", Help: "Leave this behavior disabled."},
	}, defaultValue, componentInput(input), strings.Split(corecomponent.RenderSection(title, implication), "\n")...)
	if err != nil {
		return false, err
	}
	return selected == "yes", nil
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

func reviewCreateContextName(explicitName, defaultName string, input config.InputFunc, yolo bool) (string, error) {
	defaultValue := strings.TrimSpace(explicitName)
	if defaultValue == "" {
		var err error
		defaultValue, err = resolveCreateContextName("", defaultName, func(...string) (string, error) { return "", nil })
		if err != nil {
			return "", err
		}
	}
	if yolo {
		return defaultValue, nil
	}
	return resolveRequiredCreateValue(input, "Context name", defaultValue, strings.Split(corecomponent.RenderSection(
		"sitectl context",
		"This unique name is what --context selects. It does not rename the site or any Docker Compose resources.",
	), "\n"))
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
	// Metadata discovery normally sends the RPC envelope over stdin, so callers
	// set SITECTL_RPC_METADATA because this function can only inspect argv
	// without consuming stdin. The --request path keeps interactive RPC calls
	// detectable when the envelope is intentionally sent in argv.
	if os.Getenv("SITECTL_RPC_METADATA") == "1" {
		return true
	}
	if len(os.Args) <= 1 || os.Args[1] != "__sitectl-rpc" {
		return false
	}
	for i := 2; i < len(os.Args); i++ {
		arg := os.Args[i]
		value := ""
		if arg == "--request" && i+1 < len(os.Args) {
			value = os.Args[i+1]
		} else if strings.HasPrefix(arg, "--request=") {
			value = strings.TrimPrefix(arg, "--request=")
		}
		if value == "" {
			continue
		}
		req, err := decodeRPCRequestFlag(value)
		return err == nil && req.Method == MethodPluginMetadata
	}
	return false
}

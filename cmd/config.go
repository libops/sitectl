package cmd

import (
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"

	corecomponent "github.com/libops/sitectl/pkg/component"
	"github.com/libops/sitectl/pkg/config"
	"github.com/libops/sitectl/pkg/helpers"
	"github.com/libops/sitectl/pkg/plugin"
	sitevalidate "github.com/libops/sitectl/pkg/validate"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Manage sitectl command configuration",
	Long: `
A sitectl config can have multiple contexts.

A sitectl context is a docker compose site running somewhere. "Somewhere" meaning:

- on your laptop (--type local)
- on a remote server (--type remote).

Remote contexts require SSH access to the remote server from where sitectl is being ran from.
When creating a context the remote server DNS name, SSH port, SSH username, and the path to your SSH private key will need to be set in the context configuration.

You can have a default context which will be used when running sitectl commands, unless the context is overridden with the --context flag.`,
}

var viewConfigCmd = &cobra.Command{
	Use:   "view",
	Short: "Print your sitectl config",
	Run: func(cmd *cobra.Command, args []string) {
		path := config.ConfigFilePath()
		info, err := os.Stat(path)
		if err != nil {
			if os.IsNotExist(err) {
				fmt.Printf("File %q does not exist.\n", path)
				return
			}
			log.Fatalf("Error checking file: %v", err)
		}

		// Check if it's a regular file.
		if !info.Mode().IsRegular() {
			log.Fatalf("%q is not a regular file", path)
		}

		data, err := os.ReadFile(path)
		if err != nil {
			log.Fatalf("Error reading file: %v", err)
		}

		fmt.Println(string(data))
	},
}

var currentContextCmd = &cobra.Command{
	Use:   "current-context",
	Short: "Display the current site context",
	Run: func(cmd *cobra.Command, args []string) {
		c, err := config.Current()
		if err != nil {
			log.Fatal(err)
		}
		if c == "" {
			fmt.Println("No current context is set")
		} else {
			fmt.Println("Current context:", c)
		}
	},
}

var getContextsCmd = &cobra.Command{
	Use:   "get-contexts",
	Short: "List all site contexts",
	Run: func(cmd *cobra.Command, args []string) {
		cfg, err := config.Load()
		if err != nil {
			log.Fatal(err)
		}
		if len(cfg.Contexts) == 0 {
			fmt.Println("No contexts available")
			return
		}
		writeContextTable(cmd.OutOrStdout(), cfg)
	},
}

var getSitesCmd = &cobra.Command{
	Use:   "get-sites",
	Short: "List configured sites and their environments",
	Run: func(cmd *cobra.Command, args []string) {
		cfg, err := config.Load()
		if err != nil {
			log.Fatal(err)
		}
		if len(cfg.Contexts) == 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "No sites available")
			return
		}

		sites := map[string][]config.Context{}
		for _, ctx := range cfg.Contexts {
			site := firstNonEmptyString(ctx.Site, "-")
			sites[site] = append(sites[site], ctx)
		}

		names := make([]string, 0, len(sites))
		for name := range sites {
			names = append(names, name)
		}
		sort.Strings(names)

		w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "SITE\tPLUGIN\tENVIRONMENTS\tCONTEXTS")
		for _, site := range names {
			contexts := sites[site]
			sort.Slice(contexts, func(i, j int) bool {
				return contexts[i].Name < contexts[j].Name
			})
			plugin := firstNonEmptyString(contexts[0].Plugin, "-")
			envs := uniqueSortedContextValues(contexts, func(ctx config.Context) string {
				return firstNonEmptyString(ctx.Environment, "-")
			})
			names := make([]string, 0, len(contexts))
			for _, ctx := range contexts {
				label := ctx.Name
				if ctx.Name == cfg.CurrentContext {
					label += " *"
				}
				names = append(names, label)
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
				site,
				plugin,
				strings.Join(envs, ", "),
				strings.Join(names, ", "),
			)
		}
		_ = w.Flush()
	},
}

var getEnvironmentsCmd = &cobra.Command{
	Use:   "get-environments [site]",
	Short: "List environments grouped by site",
	Args:  cobra.MaximumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		cfg, err := config.Load()
		if err != nil {
			log.Fatal(err)
		}
		if len(cfg.Contexts) == 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "No environments available")
			return
		}

		siteFilter := ""
		if len(args) == 1 {
			siteFilter = strings.TrimSpace(args[0])
		}

		contexts := make([]config.Context, 0, len(cfg.Contexts))
		for _, ctx := range cfg.Contexts {
			if siteFilter != "" && !strings.EqualFold(ctx.Site, siteFilter) {
				continue
			}
			contexts = append(contexts, ctx)
		}
		if len(contexts) == 0 {
			if siteFilter == "" {
				fmt.Fprintln(cmd.OutOrStdout(), "No environments available")
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "No environments found for site %q\n", siteFilter)
			}
			return
		}

		sort.Slice(contexts, func(i, j int) bool {
			if contexts[i].Site != contexts[j].Site {
				return contexts[i].Site < contexts[j].Site
			}
			if contexts[i].Environment != contexts[j].Environment {
				return contexts[i].Environment < contexts[j].Environment
			}
			return contexts[i].Name < contexts[j].Name
		})

		w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "SITE\tENVIRONMENT\tCONTEXT\tPLUGIN\tTYPE\tHOST")
		for _, ctx := range contexts {
			host := "-"
			if ctx.DockerHostType == config.ContextRemote {
				host = firstNonEmptyString(ctx.SSHHostname, "-")
			}
			name := ctx.Name
			if ctx.Name == cfg.CurrentContext {
				name += " *"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
				firstNonEmptyString(ctx.Site, "-"),
				firstNonEmptyString(ctx.Environment, "-"),
				name,
				firstNonEmptyString(ctx.Plugin, "-"),
				firstNonEmptyString(string(ctx.DockerHostType), "-"),
				host,
			)
		}
		_ = w.Flush()
	},
}

var (
	configValidateAll    bool
	configValidateSite   string
	configValidateFormat string
)

var validateConfigCmd = &cobra.Command{
	Use:   "validate [context-name]",
	Args:  cobra.MaximumNArgs(1),
	Short: "Validate sitectl context configuration and access",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		contexts, err := resolveValidationContexts(cfg, args)
		if err != nil {
			return err
		}
		if len(contexts) == 0 {
			return fmt.Errorf("no contexts selected for validation")
		}

		reports := make([]sitevalidate.Report, 0, len(contexts))
		valid := true
		for i := range contexts {
			results, err := sitevalidate.Run(&contexts[i], sitevalidate.CoreValidators(cfg)...)
			if err != nil {
				return err
			}
			sitevalidate.SortResults(results)
			report := sitevalidate.NewReport(&contexts[i], results)
			if !report.Valid {
				valid = false
			}
			reports = append(reports, report)
		}

		if err := sitevalidate.WriteReports(cmd.OutOrStdout(), reports, configValidateFormat); err != nil {
			return err
		}
		if !valid {
			return fmt.Errorf("validation failed")
		}
		return nil
	},
}

var setContextCmd = &cobra.Command{
	Use:   "set-context [context-name]",
	Short: "Set or update properties of a context. Creates a new context if it does not exist.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		context, err := config.GetContext(args[0])
		if err != nil {
			if !errors.Is(err, config.ErrContextNotFound) {
				return err
			}
			context = config.Context{Name: args[0]}
		}

		f := cmd.Flags()
		cc, err := config.LoadFromFlags(f, context)
		if err != nil {
			return err
		}
		cc.Name = args[0]

		defaultContext, err := f.GetBool("default")
		if err != nil {
			return err
		}

		// override local defaults for remote environments
		switch cc.DockerHostType {
		case config.ContextRemote:
			err = cc.VerifyRemoteInput(true)
			if err != nil {
				return err
			}
		case config.ContextLocal:
			cc.SSHKeyPath = ""
			cc.DockerSocket = config.GetDefaultLocalDockerSocket(cc.DockerSocket)
		default:
			slog.Error("Unknown context type", "type", cc.DockerHostType)
			os.Exit(1)
		}

		if err = config.SaveContext(cc, defaultContext); err != nil {
			helpers.ExitOnError(err)
		}

		return nil
	},
}

var useContextCmd = &cobra.Command{
	Use:   "use-context [context-name]",
	Short: "Switch to the specified context",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		name := args[0]
		cfg, err := config.Load()
		if err != nil {
			log.Fatal(err)
		}
		found := false
		for _, ctx := range cfg.Contexts {
			if ctx.Name == name {
				found = true
				break
			}
		}
		if !found {
			log.Fatalf("Context %s not found", name)
		}
		cfg.CurrentContext = name
		if err = config.Save(cfg); err != nil {
			log.Fatal(err)
		}
		fmt.Println("Switched to context:", name)
	},
}

var deleteContextCmd = &cobra.Command{
	Use:   "delete-context [context-name]",
	Short: "Delete a site context",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		name := args[0]
		cfg, err := config.Load()
		if err != nil {
			log.Fatal(err)
		}

		if cfg.CurrentContext == name {
			slog.Error("Cannot delete the current context. You can update it or create a new context with `sitectl config set-context`")
			return
		}

		found := false
		var newContexts []config.Context
		for _, ctx := range cfg.Contexts {
			if ctx.Name == name {
				found = true
				continue
			}
			newContexts = append(newContexts, ctx)
		}
		if !found {
			log.Fatalf("Context %s not found", name)
		}
		cfg.Contexts = newContexts

		if err = config.Save(cfg); err != nil {
			log.Fatal(err)
		}
		fmt.Printf("Deleted context: %s\n", name)
	},
}

var createConfigInput = config.GetInput
var createConfigPromptChoice = corecomponent.PromptChoice
var createConfigDiscoverPlugins = plugin.DiscoverInstalled
var createConfigVerifyRemote = func(ctx *config.Context) error {
	return ctx.VerifyRemoteInput(true)
}
var createConfigProjectDirExists = func(ctx *config.Context) (bool, error) {
	return ctx.ProjectDirExists()
}
var createConfigRunComposePS = func(ctx *config.Context) error {
	return ctx.ValidateComposeAccess()
}

// createConfigCmd creates sitectl config for existing isle-site-template installs
var createConfigCmd = &cobra.Command{
	Use:   "create [context-name]",
	Args:  cobra.RangeArgs(0, 1),
	Short: "Create a sitectl config for existing Docker Compose installs",
	Long: `Create a sitectl context for an existing Docker Compose installation.

This command registers an existing Docker Compose site with sitectl so you can manage it.
It does NOT create a new Docker Compose site - use 'create context' for that.

The command will interactively prompt for:
  - Whether the site is local or remote
  - Project directory path
  - Remote SSH connection details (if applicable)

Examples:
  # Create config for a local Docker Compose site
  sitectl create config dev --type local --project-dir /home/user/isle

  # Create config for a remote Docker Compose site
  sitectl create config prod \
    --type remote \
    --project-dir /opt/isle \
    --ssh-hostname isle.example.com \
    --ssh-user deploy \
    --ssh-key ~/.ssh/id_rsa`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runCreateConfig(cmd, args)
	},
}

func runCreateConfig(cmd *cobra.Command, args []string) error {
	f := cmd.Flags()
	defaultContext, err := f.GetBool("default")
	if err != nil {
		return err
	}

	if len(args) == 0 {
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		if existing, err := config.FindLocalContextByProjectDir(cwd); err != nil {
			return err
		} else if existing != nil {
			return fmt.Errorf("current directory is already registered as local context %q", existing.Name)
		}
		if config.LooksLikeComposeProject(cwd) {
			fmt.Fprintln(cmd.OutOrStdout(), corecomponent.RenderIntroSection(
				"Create a sitectl config for existing Docker Compose installs",
				"Detected a Docker Compose project in the current directory. This flow will register it as a local sitectl context.",
			))
			fmt.Fprintln(cmd.OutOrStdout())
			ctx, err := config.PromptAndSaveLocalContext(config.LocalContextCreateOptions{
				DefaultName:         filepath.Base(cwd),
				Site:                filepath.Base(cwd),
				DefaultSite:         filepath.Base(cwd),
				Plugin:              "core",
				DefaultPlugin:       "core",
				ProjectDir:          cwd,
				DefaultProjectDir:   cwd,
				DefaultProjectName:  filepath.Base(cwd),
				Environment:         "local",
				SetDefault:          defaultContext,
				Input:               createConfigInput,
				ProjectDirValidator: config.ValidateExistingComposeProjectDir,
				ContextNamePrompt: append(
					strings.Split(corecomponent.RenderSection("sitectl context name", "This is the saved sitectl target for this project. A good pattern is <site>-<environment>, for example museum-local or museum-prod."), "\n"),
					"",
					corecomponent.RenderPromptLine("Context name [%s]: "),
				),
				ProjectDirPrompt: append(
					strings.Split(corecomponent.RenderSection("Project directory", "Confirm the full directory path where this existing Docker Compose project lives."), "\n"),
					"",
					corecomponent.RenderPromptLine("Project directory [%s]: "),
				),
			})
			if err != nil {
				return err
			}
			if !f.Changed("plugin") {
				selectedPlugin, pluginErr := promptContextPlugin(ctx.Plugin)
				if pluginErr != nil {
					return pluginErr
				}
				if strings.TrimSpace(selectedPlugin) != "" && selectedPlugin != ctx.Plugin {
					ctx.Plugin = selectedPlugin
					if err := config.SaveContext(ctx, defaultContext); err != nil {
						return err
					}
				}
			}
			writeCreatedContextSummary(cmd, "Context created successfully", ctx)
			if err := promptAdditionalEnvironmentContexts(cmd, ctx); err != nil {
				return err
			}
			return nil
		}

		contextName, err := createConfigInput(
			append(
				strings.Split(corecomponent.RenderSection(
					"sitectl context name",
					"Provide a admin label for this site and environment. Only provide alpha numeric characters and dashes. A good pattern is <site>-<environment>, for example museum-local or museum-prod.",
				), "\n"),
				"",
				corecomponent.RenderPromptLine("Context name: "),
			)...,
		)
		if err != nil {
			return err
		}
		contextName = strings.TrimSpace(contextName)
		if contextName == "" {
			return fmt.Errorf("context name cannot be empty")
		}
		args = []string{contextName}
	}

	cc, err := config.GetContext(args[0])
	if err != nil {
		if !errors.Is(err, config.ErrContextNotFound) {
			return err
		}
		cc = config.Context{Name: args[0]}
	}

	cexists := !errors.Is(err, config.ErrContextNotFound)
	context, err := config.LoadFromFlags(f, cc)
	if err != nil {
		return err
	}
	if !cexists {
		if active, activeErr := config.CurrentContext(f); activeErr == nil && active != nil && active.Name != args[0] {
			inheritNewContextDefaultsFromActive(context, active, f)
		}
	}
	context.Name = args[0]
	if strings.TrimSpace(context.Plugin) == "" {
		context.Plugin = "core"
	}

	if cexists {
		overwrite, err := createConfigInput("The context already exists. Do you want to overwrite it? [y/N]: ")
		if err != nil {
			return err
		}
		if !strings.EqualFold(overwrite, "y") && !strings.EqualFold(overwrite, "yes") {
			return fmt.Errorf("context creation cancelled")
		}
	}

	if !f.Changed("type") {
		t, err := createConfigInput(fmt.Sprintf("Is the context local (on this machine) or remote (on a VM)? [%s]: ", string(context.DockerHostType)))
		if err != nil {
			return err
		}
		if t != "" {
			if t != "remote" && t != "local" {
				return fmt.Errorf("unknown context type %q: valid values are local or remote", t)
			}
			context.DockerHostType = config.ContextType(t)
		}
	}
	if !f.Changed("project-dir") {
		dir, err := createConfigInput(fmt.Sprintf("Full directory path to the project (directory where docker-compose.yml is located) [%s]: ", context.ProjectDir))
		if err != nil {
			return err
		}
		if dir != "" {
			context.ProjectDir = dir
		}
	}
	context.ProjectDir, err = config.ExpandProjectDir(context.ProjectDir)
	if err != nil {
		return err
	}
	if context.DockerHostType == config.ContextLocal && strings.TrimSpace(context.Environment) == "" {
		context.Environment = "local"
	}
	if !f.Changed("plugin") && (strings.TrimSpace(context.Plugin) == "" || context.Plugin == "core") {
		selectedPlugin, pluginErr := promptContextPlugin(context.Plugin)
		if pluginErr != nil {
			return pluginErr
		}
		if strings.TrimSpace(selectedPlugin) != "" {
			context.Plugin = selectedPlugin
		}
	}
	if !f.Changed("project-name") && placeholderProjectName(context.ProjectName) {
		context.ProjectName = firstNonEmptyString(filepath.Base(context.ProjectDir), "docker-compose")
	}
	if strings.TrimSpace(context.ProjectName) == "" {
		context.ProjectName = firstNonEmptyString(filepath.Base(context.ProjectDir), "docker-compose")
	}
	if !f.Changed("compose-project-name") && strings.TrimSpace(context.ComposeProjectName) == "" {
		context.ComposeProjectName = firstNonEmptyString(config.DetectComposeProjectName(context.ProjectDir), context.ProjectName)
	}
	if !f.Changed("compose-network") && strings.TrimSpace(context.ComposeNetwork) == "" {
		context.ComposeNetwork = config.DetectComposeNetworkName(context.ProjectDir, context.EffectiveComposeProjectName())
	}
	if !f.Changed("site") && placeholderProjectName(context.Site) {
		context.Site = firstNonEmptyString(filepath.Base(context.ProjectDir), context.ProjectName, context.Name)
	}
	if strings.TrimSpace(context.Site) == "" {
		context.Site = firstNonEmptyString(filepath.Base(context.ProjectDir), context.ProjectName, context.Name)
	}

	if context.DockerHostType == config.ContextRemote {
		if strings.TrimSpace(context.DockerSocket) == "" {
			context.DockerSocket = "/var/run/docker.sock"
		}
		err = createConfigVerifyRemote(context)
		if err != nil {
			return err
		}
	} else if !f.Changed("docker-socket") {
		context.DockerSocket = config.GetDefaultLocalDockerSocket(context.DockerSocket)
	}
	exists, err := createConfigProjectDirExists(context)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("project directory %q does not exist", context.ProjectDir)
	}
	if context.DockerHostType == config.ContextRemote {
		if err := validateRemoteDockerAccess(context); err != nil {
			return err
		}
	}

	if err := config.SaveContext(context, defaultContext); err != nil {
		return err
	}
	writeCreatedContextSummary(cmd, "Context created successfully", context)
	if context.DockerHostType == config.ContextLocal {
		if err := promptAdditionalEnvironmentContexts(cmd, context); err != nil {
			return err
		}
	}
	return nil
}

func placeholderProjectName(value string) bool {
	value = strings.TrimSpace(value)
	return value == "" || value == "docker-compose"
}

func promptContextPlugin(defaultPlugin string) (string, error) {
	choices := []corecomponent.Choice{{
		Value:   "core",
		Label:   "core",
		Help:    "No stack-specific plugin. Use base sitectl behavior.",
		Aliases: []string{"1"},
	}}
	for _, discovered := range createConfigDiscoverPlugins() {
		name := strings.TrimSpace(discovered.Name)
		if name == "" || name == "core" {
			continue
		}
		choices = append(choices, corecomponent.Choice{
			Value:   name,
			Label:   name,
			Help:    firstNonEmptyString(discovered.Description, "Use the "+name+" plugin for this site."),
			Aliases: nil,
		})
	}
	if len(choices) == 1 {
		return firstNonEmptyString(defaultPlugin, "core"), nil
	}
	selected, err := createConfigPromptChoice(
		"plugin",
		choices,
		firstNonEmptyString(strings.TrimSpace(defaultPlugin), "core"),
		createConfigInput,
		strings.Split(corecomponent.RenderSection("plugin", "If this project belongs to a known sitectl plugin, pick it here. For example, an Islandora stack would usually use the isle plugin."), "\n")...,
	)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(selected), nil
}

func promptAdditionalEnvironmentContexts(cmd *cobra.Command, localCtx *config.Context) error {
	if localCtx == nil || localCtx.DockerHostType != config.ContextLocal {
		return nil
	}

	var previousRemote *config.Context
	for {
		answer, err := createConfigPromptChoice(
			"add-environment",
			[]corecomponent.Choice{
				{
					Value:   "yes",
					Label:   "yes",
					Help:    "Create another environment context for this site.",
					Aliases: []string{"y", "1"},
				},
				{
					Value:   "no",
					Label:   "no",
					Help:    "Finish without adding more environment contexts.",
					Aliases: []string{"n", "2"},
				},
			},
			"no",
			createConfigInput,
			strings.Split(corecomponent.RenderSection("Additional environments", "Add another environment for this site?"), "\n")...,
		)
		if err != nil {
			return err
		}
		if strings.TrimSpace(answer) != "yes" {
			return nil
		}

		remoteCtx, err := promptRemoteEnvironmentContext(localCtx, previousRemote)
		if err != nil {
			return err
		}
		if err := config.SaveContext(remoteCtx, false); err != nil {
			return err
		}
		previousRemote = remoteCtx
		writeCreatedContextSummary(cmd, "Environment context created successfully", remoteCtx)
	}
}

func promptRemoteEnvironmentContext(localCtx, previousRemote *config.Context) (*config.Context, error) {
	environment, err := promptRequiredValue("Environment name (e.g. dev, staging, prod): ")
	if err != nil {
		return nil, err
	}

	defaultName := suggestedEnvironmentContextName(localCtx, environment)
	name, err := createConfigInput(fmt.Sprintf("Context name [%s]: ", defaultName))
	if err != nil {
		return nil, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = defaultName
	}

	projectDir, err := promptRequiredValueWithDefault(
		"Full directory path to the remote project (directory where docker-compose.yml is located)",
		firstNonEmptyString(remoteContextValue(previousRemote, func(ctx *config.Context) string { return ctx.ProjectDir })),
	)
	if err != nil {
		return nil, err
	}

	currentUser := ""
	if u, err := user.Current(); err == nil {
		currentUser = u.Username
	}
	defaultKey := filepath.Join(os.Getenv("HOME"), ".ssh", "id_rsa")
	hostname := ""
	sshUser := firstNonEmptyString(remoteContextValue(previousRemote, func(ctx *config.Context) string { return ctx.SSHUser }), currentUser, "root")
	sshPort := remoteContextUint(previousRemote, func(ctx *config.Context) uint { return ctx.SSHPort }, 22)
	sshKey := firstNonEmptyString(remoteContextValue(previousRemote, func(ctx *config.Context) string { return ctx.SSHKeyPath }), defaultKey)
	dockerSocket := firstNonEmptyString(remoteContextValue(previousRemote, func(ctx *config.Context) string { return ctx.DockerSocket }), "/var/run/docker.sock")
	projectName := firstNonEmptyString(remoteContextValue(previousRemote, func(ctx *config.Context) string { return ctx.ProjectName }), localCtx.ProjectName, "docker-compose")
	composeProjectName := firstNonEmptyString(
		remoteContextValue(previousRemote, func(ctx *config.Context) string { return ctx.ComposeProjectName }),
		localCtx.EffectiveComposeProjectName(),
		projectName,
	)
	composeNetwork := firstNonEmptyString(
		remoteContextValue(previousRemote, func(ctx *config.Context) string { return ctx.ComposeNetwork }),
		localCtx.ComposeNetwork,
		localCtx.EffectiveComposeNetwork(),
	)
	for {
		hostname, err = promptRequiredValueWithDefault("Remote hostname/domain (e.g. stage.example.com)", hostname)
		if err != nil {
			return nil, err
		}
		sshUser, err = promptRequiredValueWithDefault("SSH user", sshUser)
		if err != nil {
			return nil, err
		}
		sshPort, err = promptUintWithDefault("SSH port", sshPort)
		if err != nil {
			return nil, err
		}
		sshKey, err = promptRequiredValueWithDefault("Path to SSH private key", sshKey)
		if err != nil {
			return nil, err
		}

		remoteCtx := &config.Context{
			Name:                   name,
			Site:                   firstNonEmptyString(localCtx.Site, localCtx.ProjectName, localCtx.Name),
			Plugin:                 firstNonEmptyString(localCtx.Plugin, "core"),
			DockerHostType:         config.ContextRemote,
			Environment:            environment,
			ProjectDir:             strings.TrimSpace(projectDir),
			ProjectName:            firstNonEmptyString(localCtx.ProjectName, "docker-compose"),
			ComposeProjectName:     composeProjectName,
			ComposeNetwork:         composeNetwork,
			SSHHostname:            strings.TrimSpace(hostname),
			SSHUser:                sshUser,
			SSHPort:                sshPort,
			SSHKeyPath:             sshKey,
			DockerSocket:           dockerSocket,
			ComposeFile:            append([]string{}, localCtx.ComposeFile...),
			EnvFile:                append([]string{}, localCtx.EnvFile...),
			DatabaseService:        localCtx.DatabaseService,
			DatabaseUser:           localCtx.DatabaseUser,
			DatabasePasswordSecret: localCtx.DatabasePasswordSecret,
			DatabaseName:           localCtx.DatabaseName,
		}
		remoteCtx.ProjectName = projectName
		remoteCtx.ComposeProjectName = composeProjectName
		remoteCtx.ComposeNetwork = composeNetwork
		if detected := config.DetectContextComposeNetwork(remoteCtx); detected != "" {
			remoteCtx.ComposeNetwork = detected
		}

		if err := createConfigVerifyRemote(remoteCtx); err != nil {
			retry, retryErr := createConfigPromptChoice(
				"retry-environment-connection",
				[]corecomponent.Choice{
					{
						Value:   "retry",
						Label:   "retry",
						Help:    "Re-enter the remote connection details for this environment.",
						Aliases: []string{"y", "yes", "1"},
					},
					{
						Value:   "cancel",
						Label:   "cancel",
						Help:    "Stop adding this environment context.",
						Aliases: []string{"n", "no", "2"},
					},
				},
				"retry",
				createConfigInput,
				strings.Split(corecomponent.RenderSection("Remote connection failed", err.Error()), "\n")...,
			)
			if retryErr != nil {
				return nil, retryErr
			}
			if strings.TrimSpace(retry) != "retry" {
				return nil, err
			}
			continue
		}
		exists, err := createConfigProjectDirExists(remoteCtx)
		if err != nil {
			return nil, err
		}
		if !exists {
			return nil, fmt.Errorf("project directory %q does not exist", remoteCtx.ProjectDir)
		}
		if err := validateRemoteDockerAccess(remoteCtx); err != nil {
			return nil, err
		}
		return remoteCtx, nil
	}
}

func promptRequiredValue(prompt string) (string, error) {
	value, err := createConfigInput(prompt)
	if err != nil {
		return "", err
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("value cannot be empty")
	}
	return value, nil
}

func promptRequiredValueWithDefault(label, defaultValue string) (string, error) {
	value, err := createConfigInput(fmt.Sprintf("%s [%s]: ", label, defaultValue))
	if err != nil {
		return "", err
	}
	value = strings.TrimSpace(value)
	if value == "" {
		value = strings.TrimSpace(defaultValue)
	}
	if value == "" {
		return "", fmt.Errorf("value cannot be empty")
	}
	return value, nil
}

func promptUintWithDefault(label string, defaultValue uint) (uint, error) {
	value, err := createConfigInput(fmt.Sprintf("%s [%d]: ", label, defaultValue))
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

func validateRemoteDockerAccess(ctx *config.Context) error {
	if ctx == nil || ctx.DockerHostType != config.ContextRemote {
		return nil
	}
	for {
		fmt.Fprintln(os.Stdout)
		fmt.Fprintln(os.Stdout, corecomponent.RenderSection("Remote docker validation", fmt.Sprintf("Checking docker compose access for `%s` with `docker compose ps`.", ctx.Name)))
		if err := createConfigRunComposePS(ctx); err != nil {
			action, actionErr := createConfigPromptChoice(
				"update-environment-context",
				[]corecomponent.Choice{
					{
						Value:   "update",
						Label:   "update",
						Help:    "Update the remote Docker settings and try compose ps again.",
						Aliases: []string{"y", "yes", "1"},
					},
					{
						Value:   "cancel",
						Label:   "cancel",
						Help:    "Stop using this remote context configuration.",
						Aliases: []string{"n", "no", "2"},
					},
				},
				"update",
				createConfigInput,
				strings.Split(corecomponent.RenderSection("Remote docker validation failed", err.Error()), "\n")...,
			)
			if actionErr != nil {
				return actionErr
			}
			if strings.TrimSpace(action) != "update" {
				return err
			}
			projectDir, promptErr := promptRequiredValueWithDefault("Full directory path to the remote project (directory where docker-compose.yml is located)", ctx.ProjectDir)
			if promptErr != nil {
				return promptErr
			}
			projectName, promptErr := promptRequiredValueWithDefault("Logical project name", firstNonEmptyString(ctx.ProjectName, "docker-compose"))
			if promptErr != nil {
				return promptErr
			}
			dockerSocket, promptErr := promptRequiredValueWithDefault("Docker socket", firstNonEmptyString(ctx.DockerSocket, "/var/run/docker.sock"))
			if promptErr != nil {
				return promptErr
			}
			ctx.ProjectDir = projectDir
			ctx.ProjectName = projectName
			ctx.ComposeProjectName = firstNonEmptyString(ctx.ComposeProjectName, projectName)
			ctx.ComposeNetwork = firstNonEmptyString(config.DetectContextComposeNetwork(ctx), ctx.ComposeNetwork, ctx.EffectiveComposeNetwork())
			ctx.DockerSocket = dockerSocket
			continue
		}
		return nil
	}
}

func writeCreatedContextSummary(cmd *cobra.Command, title string, ctx *config.Context) {
	if cmd == nil || ctx == nil {
		return
	}
	contextStr, err := ctx.String()
	if err != nil {
		fmt.Fprintln(cmd.OutOrStdout(), corecomponent.RenderSection(title, ctx.Name))
		return
	}
	fmt.Fprintln(cmd.OutOrStdout())
	fmt.Fprintln(cmd.OutOrStdout(), corecomponent.RenderSection(title, strings.TrimSpace(contextStr)))
}

func remoteContextValue(ctx *config.Context, getter func(*config.Context) string) string {
	if ctx == nil || getter == nil {
		return ""
	}
	return strings.TrimSpace(getter(ctx))
}

func remoteContextUint(ctx *config.Context, getter func(*config.Context) uint, fallback uint) uint {
	if ctx == nil || getter == nil {
		return fallback
	}
	if value := getter(ctx); value != 0 {
		return value
	}
	return fallback
}

func suggestedEnvironmentContextName(localCtx *config.Context, environment string) string {
	base := strings.TrimSpace(environment)
	if localCtx == nil {
		return base
	}
	name := strings.TrimSpace(localCtx.Name)
	if strings.HasSuffix(name, "-local") {
		return strings.TrimSuffix(name, "-local") + "-" + environment
	}
	if strings.TrimSpace(localCtx.ProjectName) != "" && localCtx.ProjectName != "docker-compose" {
		return localCtx.ProjectName + "-" + environment
	}
	if name != "" {
		return name + "-" + environment
	}
	return base
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func writeContextTable(out io.Writer, cfg *config.Config) {
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "CURRENT\tCONTEXT\tSITE\tPLUGIN\tENVIRONMENT\tTYPE\tPROJECT")
	for _, ctx := range cfg.Contexts {
		activeMark := ""
		if ctx.Name == cfg.CurrentContext {
			activeMark = "*"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			activeMark,
			ctx.Name,
			firstNonEmptyString(ctx.Site, "-"),
			firstNonEmptyString(ctx.Plugin, "-"),
			firstNonEmptyString(ctx.Environment, "-"),
			firstNonEmptyString(string(ctx.DockerHostType), "-"),
			firstNonEmptyString(ctx.ProjectName, "-"),
		)
	}
	_ = w.Flush()
}

func resolveValidationContexts(cfg *config.Config, args []string) ([]config.Context, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config is nil")
	}
	if configValidateAll {
		return filterValidationContexts(cfg.Contexts), nil
	}
	if strings.TrimSpace(configValidateSite) != "" {
		selected := []config.Context{}
		for _, ctx := range cfg.Contexts {
			if strings.EqualFold(ctx.Site, configValidateSite) {
				selected = append(selected, ctx)
			}
		}
		if len(selected) == 0 {
			return nil, fmt.Errorf("no contexts found for site %q", configValidateSite)
		}
		return selected, nil
	}
	if len(args) == 1 {
		ctx, err := config.GetContext(args[0])
		if err != nil {
			return nil, err
		}
		return []config.Context{ctx}, nil
	}
	current, err := config.Current()
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(current) == "" {
		return nil, fmt.Errorf("no current context is set")
	}
	ctx, err := config.GetContext(current)
	if err != nil {
		return nil, err
	}
	return []config.Context{ctx}, nil
}

func filterValidationContexts(contexts []config.Context) []config.Context {
	selected := append([]config.Context{}, contexts...)
	sort.Slice(selected, func(i, j int) bool {
		if selected[i].Site != selected[j].Site {
			return selected[i].Site < selected[j].Site
		}
		if selected[i].Environment != selected[j].Environment {
			return selected[i].Environment < selected[j].Environment
		}
		return selected[i].Name < selected[j].Name
	})
	return selected
}

func uniqueSortedContextValues(contexts []config.Context, getter func(config.Context) string) []string {
	values := map[string]struct{}{}
	for _, ctx := range contexts {
		value := strings.TrimSpace(getter(ctx))
		if value == "" {
			continue
		}
		values[value] = struct{}{}
	}
	ordered := make([]string, 0, len(values))
	for value := range values {
		ordered = append(ordered, value)
	}
	sort.Strings(ordered)
	return ordered
}

func inheritNewContextDefaultsFromActive(target, active *config.Context, flags *pflag.FlagSet) {
	if target == nil || active == nil || flags == nil {
		return
	}
	if !flags.Changed("site") && target.Site == "" && active.Site != "" {
		target.Site = active.Site
	}
	if !flags.Changed("plugin") && (target.Plugin == "" || target.Plugin == "core") && active.Plugin != "" {
		target.Plugin = active.Plugin
	}
	if !flags.Changed("project-name") && active.ProjectName != "" && target.ProjectName == "docker-compose" {
		target.ProjectName = active.ProjectName
	}
	if !flags.Changed("compose-project-name") && active.ComposeProjectName != "" && target.ComposeProjectName == "" {
		target.ComposeProjectName = active.ComposeProjectName
	}
	if !flags.Changed("compose-network") && active.ComposeNetwork != "" && target.ComposeNetwork == "" {
		target.ComposeNetwork = active.ComposeNetwork
	}
	if !flags.Changed("compose-file") && len(target.ComposeFile) == 0 && len(active.ComposeFile) > 0 {
		target.ComposeFile = append([]string{}, active.ComposeFile...)
	}
	if !flags.Changed("env-file") && len(target.EnvFile) == 0 && len(active.EnvFile) > 0 {
		target.EnvFile = append([]string{}, active.EnvFile...)
	}
	if !flags.Changed("database-service") && target.DatabaseService == "mariadb" && active.DatabaseService != "" {
		target.DatabaseService = active.DatabaseService
	}
	if !flags.Changed("database-user") && target.DatabaseUser == "root" && active.DatabaseUser != "" {
		target.DatabaseUser = active.DatabaseUser
	}
	if !flags.Changed("database-password-secret") && target.DatabasePasswordSecret == "DB_ROOT_PASSWORD" && active.DatabasePasswordSecret != "" {
		target.DatabasePasswordSecret = active.DatabasePasswordSecret
	}
	if !flags.Changed("database-name") && target.DatabaseName == "drupal_default" && active.DatabaseName != "" {
		target.DatabaseName = active.DatabaseName
	}
}

func init() {
	setFlags := setContextCmd.Flags()
	config.SetCommandFlags(setFlags)
	setFlags.Bool("default", false, "set to default context")

	createFlags := createConfigCmd.Flags()
	config.SetCommandFlags(createFlags)
	createFlags.Bool("default", false, "set to default context")

	validateConfigCmd.Flags().BoolVar(&configValidateAll, "all", false, "Validate all configured contexts")
	validateConfigCmd.Flags().StringVar(&configValidateSite, "site", "", "Validate all contexts for a specific site")
	corecomponent.AddReportFlags(validateConfigCmd, nil, &configValidateFormat)

	configCmd.AddCommand(viewConfigCmd)
	configCmd.AddCommand(createConfigCmd)
	configCmd.AddCommand(currentContextCmd)
	configCmd.AddCommand(getContextsCmd)
	configCmd.AddCommand(getSitesCmd)
	configCmd.AddCommand(getEnvironmentsCmd)
	configCmd.AddCommand(validateConfigCmd)
	configCmd.AddCommand(setContextCmd)
	configCmd.AddCommand(useContextCmd)
	configCmd.AddCommand(deleteContextCmd)
	RootCmd.AddCommand(configCmd)
}

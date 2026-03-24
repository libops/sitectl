package cmd

import (
	"fmt"
	"sort"
	"strings"
	"text/tabwriter"

	corecomponent "github.com/libops/sitectl/pkg/component"
	"github.com/libops/sitectl/pkg/config"
	"github.com/libops/sitectl/pkg/plugin"
	"github.com/spf13/cobra"
)

var (
	createDiscoverPlugins = plugin.DiscoverInstalled
	createPromptChoice    = corecomponent.PromptChoice
	createPromptInput     = config.GetInput
)

var createCmd = &cobra.Command{
	Use:                "create [plugin[/definition]] [args...]",
	Short:              "Create a new stack from an installed plugin definition",
	Long:               "Create a new stack using a first-class create definition registered by an installed sitectl plugin.",
	DisableFlagParsing: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) > 0 && args[0] == "list" {
			return runCreateList(cmd)
		}
		return runCreate(cmd, args)
	},
}

func init() {
	createCmd.GroupID = "setup"
	RootCmd.AddCommand(createCmd)
}

func runCreateList(cmd *cobra.Command) error {
	plugins := availableCreatePlugins()
	if len(plugins) == 0 {
		_, err := fmt.Fprintln(cmd.OutOrStdout(), "No create definitions found. Install a sitectl-* plugin that registers one.")
		return err
	}

	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "PLUGIN\tNAME\tDESCRIPTION\tMIN\tREPO")
	for _, installed := range plugins {
		for _, spec := range installed.CreateDefinitions {
			minimums := formatCreateMinimums(spec)
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", installed.Name, spec.Name, spec.Description, minimums, helpersFirstCreateRepo(installed, spec))
		}
	}
	return w.Flush()
}

func runCreate(cmd *cobra.Command, args []string) error {
	plugins := availableCreatePlugins()
	if len(plugins) == 0 {
		return fmt.Errorf("no create definitions found; install a sitectl-* plugin that registers one")
	}

	forwarded := []string{}
	if !createArgsContainFlag(args, "type") {
		targetType, err := promptForCreateTarget()
		if err != nil {
			return err
		}
		forwarded = append(forwarded, "--type", string(targetType))
	}

	owner, spec, remaining, err := resolveCreateInvocation(plugins, args)
	if err != nil {
		return err
	}
	if !createArgsContainFlag(remaining, "checkout-source") && !createArgsContainFlag(args, "checkout-source") {
		checkoutSource, sourceErr := promptForCheckoutSource(createForwardedTargetType(args, forwarded))
		if sourceErr != nil {
			return sourceErr
		}
		forwarded = append(forwarded, "--checkout-source", string(checkoutSource))
	}

	invokeArgs := append([]string{"__create", spec.Name}, append(forwarded, remaining...)...)
	_, err = pluginSDK.InvokePluginCommand(owner, invokeArgs, plugin.CommandExecOptions{
		Context: RootCmd.Context(),
		Stdin:   cmd.InOrStdin(),
		Stdout:  cmd.OutOrStdout(),
		Stderr:  cmd.ErrOrStderr(),
	})
	if err != nil {
		return cleanPluginCommandError(err)
	}
	return nil
}

func availableCreatePlugins() []plugin.InstalledPlugin {
	plugins := createDiscoverPlugins()
	filtered := make([]plugin.InstalledPlugin, 0, len(plugins))
	for _, installed := range plugins {
		if len(installed.CreateDefinitions) == 0 {
			continue
		}
		filtered = append(filtered, installed)
	}
	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].Name < filtered[j].Name
	})
	return filtered
}

func resolveCreateInvocation(plugins []plugin.InstalledPlugin, args []string) (string, plugin.CreateSpec, []string, error) {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		installed, err := promptForCreatePlugin(plugins)
		if err != nil {
			return "", plugin.CreateSpec{}, nil, err
		}
		spec, err := selectCreateDefinition(installed)
		return installed.Name, spec, args, err
	}

	target := strings.TrimSpace(args[0])
	remaining := args[1:]
	if pluginName, createName, ok := strings.Cut(target, "/"); ok {
		installed, found := findCreatePlugin(plugins, pluginName)
		if !found {
			return "", plugin.CreateSpec{}, nil, fmt.Errorf("plugin %q is not installed or does not define any create flows", pluginName)
		}
		spec, found := findCreateDefinition(installed.CreateDefinitions, createName)
		if !found {
			return "", plugin.CreateSpec{}, nil, fmt.Errorf("create definition %q not found for plugin %q", createName, installed.Name)
		}
		return installed.Name, spec, remaining, nil
	}

	if installed, found := findCreatePlugin(plugins, target); found {
		spec, err := selectCreateDefinition(installed)
		return installed.Name, spec, remaining, err
	}

	owner, spec, err := resolveCreateDefinitionByName(plugins, target)
	return owner, spec, remaining, err
}

func promptForCreateTarget() (config.ContextType, error) {
	selected, err := createPromptChoice(
		"create target",
		[]corecomponent.Choice{
			{Value: string(config.ContextLocal), Label: "local", Help: "Run this stack on your local machine."},
			{Value: string(config.ContextRemote), Label: "remote", Help: "Run this stack on a remote machine over SSH."},
		},
		string(config.ContextLocal),
		createPromptInput,
		strings.Split(corecomponent.RenderSection("Target machine", "Choose where this stack will run."), "\n")...,
	)
	if err != nil {
		return "", err
	}
	return config.ContextType(strings.TrimSpace(selected)), nil
}

func promptForCheckoutSource(targetType config.ContextType) (plugin.CheckoutSource, error) {
	defaultChoice := string(plugin.CheckoutSourceTemplate)
	if targetType == config.ContextRemote {
		defaultChoice = string(plugin.CheckoutSourceExisting)
	}
	selected, err := createPromptChoice(
		"checkout source",
		[]corecomponent.Choice{
			{Value: string(plugin.CheckoutSourceTemplate), Label: "template", Help: "Clone the template repository as a fresh install."},
			{Value: string(plugin.CheckoutSourceExisting), Label: "existing", Help: "Use a repo or checkout that already exists."},
		},
		defaultChoice,
		createPromptInput,
		strings.Split(corecomponent.RenderSection("Project source", "Choose whether to create from the template repository or use an existing checkout."), "\n")...,
	)
	if err != nil {
		return "", err
	}
	return plugin.CheckoutSource(strings.TrimSpace(selected)), nil
}

func promptForCreatePlugin(plugins []plugin.InstalledPlugin) (plugin.InstalledPlugin, error) {
	if len(plugins) == 1 {
		return plugins[0], nil
	}
	choices := make([]corecomponent.Choice, 0, len(plugins))
	for _, installed := range plugins {
		label := installed.Name
		help := strings.TrimSpace(installed.Description)
		if help == "" {
			help = fmt.Sprintf("Create with the %s plugin", installed.Name)
		}
		choices = append(choices, corecomponent.Choice{Value: installed.Name, Label: label, Help: help})
	}
	value, err := createPromptChoice("plugin", choices, plugins[0].Name, createPromptInput,
		strings.Split(corecomponent.RenderSection("Create plugin", "Choose which installed plugin should provision this new stack."), "\n")...,
	)
	if err != nil {
		return plugin.InstalledPlugin{}, err
	}
	installed, _ := findCreatePlugin(plugins, value)
	return installed, nil
}

func selectCreateDefinition(installed plugin.InstalledPlugin) (plugin.CreateSpec, error) {
	if spec, ok := defaultCreateDefinition(installed.CreateDefinitions); ok {
		return spec, nil
	}
	if len(installed.CreateDefinitions) == 1 {
		return installed.CreateDefinitions[0], nil
	}
	choices := make([]corecomponent.Choice, 0, len(installed.CreateDefinitions))
	for _, spec := range installed.CreateDefinitions {
		choices = append(choices, corecomponent.Choice{
			Value: spec.Name,
			Label: spec.Name,
			Help:  strings.TrimSpace(spec.Description),
		})
	}
	value, err := createPromptChoice("create definition", choices, installed.CreateDefinitions[0].Name, createPromptInput,
		strings.Split(corecomponent.RenderSection("Create definition", fmt.Sprintf("The %s plugin exposes multiple create flows. Choose one.", installed.Name)), "\n")...,
	)
	if err != nil {
		return plugin.CreateSpec{}, err
	}
	spec, _ := findCreateDefinition(installed.CreateDefinitions, value)
	return spec, nil
}

func defaultCreateDefinition(specs []plugin.CreateSpec) (plugin.CreateSpec, bool) {
	if len(specs) == 0 {
		return plugin.CreateSpec{}, false
	}
	for _, spec := range specs {
		if spec.Default {
			return spec, true
		}
	}
	if len(specs) == 1 {
		return specs[0], true
	}
	return plugin.CreateSpec{}, false
}

func resolveCreateDefinitionByName(plugins []plugin.InstalledPlugin, name string) (string, plugin.CreateSpec, error) {
	var matches []struct {
		owner string
		spec  plugin.CreateSpec
	}
	for _, installed := range plugins {
		if spec, found := findCreateDefinition(installed.CreateDefinitions, name); found {
			matches = append(matches, struct {
				owner string
				spec  plugin.CreateSpec
			}{owner: installed.Name, spec: spec})
		}
	}
	if len(matches) == 0 {
		return "", plugin.CreateSpec{}, fmt.Errorf("create target %q not found; use `sitectl create list` to see available definitions", name)
	}
	if len(matches) > 1 {
		owners := make([]string, 0, len(matches))
		for _, match := range matches {
			owners = append(owners, match.owner)
		}
		sort.Strings(owners)
		return "", plugin.CreateSpec{}, fmt.Errorf("create target %q is ambiguous; qualify it as plugin/name (%s)", name, strings.Join(owners, ", "))
	}
	return matches[0].owner, matches[0].spec, nil
}

func findCreatePlugin(plugins []plugin.InstalledPlugin, name string) (plugin.InstalledPlugin, bool) {
	for _, installed := range plugins {
		if strings.EqualFold(installed.Name, strings.TrimSpace(name)) {
			return installed, true
		}
	}
	return plugin.InstalledPlugin{}, false
}

func findCreateDefinition(specs []plugin.CreateSpec, name string) (plugin.CreateSpec, bool) {
	for _, spec := range specs {
		if strings.EqualFold(spec.Name, strings.TrimSpace(name)) {
			return spec, true
		}
	}
	return plugin.CreateSpec{}, false
}

func formatCreateMinimums(spec plugin.CreateSpec) string {
	parts := []string{}
	if spec.MinCPUCores > 0 {
		parts = append(parts, fmt.Sprintf("%.0f CPU", spec.MinCPUCores))
	}
	if strings.TrimSpace(spec.MinMemory) != "" {
		parts = append(parts, spec.MinMemory+" RAM")
	}
	if strings.TrimSpace(spec.MinDiskSpace) != "" {
		parts = append(parts, spec.MinDiskSpace+" disk")
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, ", ")
}

func helpersFirstCreateRepo(installed plugin.InstalledPlugin, spec plugin.CreateSpec) string {
	if strings.TrimSpace(spec.DockerComposeRepo) != "" {
		return spec.DockerComposeRepo
	}
	if strings.TrimSpace(installed.TemplateRepo) != "" {
		return installed.TemplateRepo
	}
	return "-"
}

func createArgsContainFlag(args []string, name string) bool {
	longFlag := "--" + name
	for i, arg := range args {
		if arg == longFlag {
			return true
		}
		if strings.HasPrefix(arg, longFlag+"=") {
			return true
		}
		if i > 0 && args[i-1] == longFlag {
			return true
		}
	}
	return false
}

func createForwardedTargetType(args, forwarded []string) config.ContextType {
	for i, arg := range append([]string{}, append(forwarded, args...)...) {
		if arg == "--type" && i+1 < len(append([]string{}, append(forwarded, args...)...)) {
			return config.ContextType(strings.TrimSpace(append([]string{}, append(forwarded, args...)...)[i+1]))
		}
		if strings.HasPrefix(arg, "--type=") {
			return config.ContextType(strings.TrimSpace(strings.TrimPrefix(arg, "--type=")))
		}
	}
	return config.ContextLocal
}

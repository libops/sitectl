package cmd

import (
	"fmt"
	"strings"

	"github.com/libops/sitectl/pkg/config"
	"github.com/libops/sitectl/pkg/helpers"
	"github.com/libops/sitectl/pkg/plugin"
	"github.com/spf13/cobra"
)

var (
	componentDescribeName        string
	componentDescribePath        string
	componentDescribeDrupalRoot  string
	componentDescribeVerbose     bool
	componentDescribeFormat      string
	componentReconcileName       string
	componentReconcilePath       string
	componentReconcileDrupalRoot string
	componentReconcileReport     bool
	componentReconcileVerbose    bool
	componentReconcileFormat     string
	componentSetPath             string
	componentSetDrupalRoot       string
	componentSetState            string
	componentSetDisposition      string
	componentSetTLSMode          string
	componentSetYolo             bool
	invokeComponentPluginCommand = func(cmd *cobra.Command, pluginName, contextName string, args []string) error {
		installed, ok := plugin.FindInstalled(pluginName)
		if !ok {
			return fmt.Errorf("plugin %q is not installed", pluginName)
		}
		invocation := make([]string, 0, len(args)+2)
		if strings.TrimSpace(contextName) != "" {
			invocation = append(invocation, "--context", contextName)
		}
		invocation = append(invocation, args...)
		_, err := pluginSDK.InvokePluginCommand(installed.Name, invocation, plugin.CommandExecOptions{
			Context: RootCmd.Context(),
			Stdin:   cmd.InOrStdin(),
			Stdout:  cmd.OutOrStdout(),
			Stderr:  cmd.ErrOrStderr(),
		})
		return err
	}
	invokePluginCommand = func(pluginName, contextName string, args []string) error {
		return invokeComponentPluginCommand(RootCmd, pluginName, contextName, args)
	}
)

var componentCmd = &cobra.Command{
	Use:   "component",
	Short: "Inspect and manage stack components for the active context",
	Long: `Components are optional stack features — such as Fcrepo or Blazegraph — that can be toggled on or off.

sitectl dispatches component commands to the plugin associated with the active context. The plugin
provides the component registry; sitectl provides a consistent entry point regardless of which stack
you are working with.

Use "sitectl component list" to show registered components, allowed dispositions, and any
component-specific flags accepted by "sitectl component set".`,
}

var componentListCmd = &cobra.Command{
	Use:                "list [component]",
	Aliases:            []string{"ls", "catalog"},
	Short:              "List components and component-specific set flags",
	Long:               "List components registered by the active context's plugin, including allowed dispositions and component-specific set flags.",
	DisableFlagParsing: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		if containsHelpArg(args) {
			return writeComponentListHelp(cmd)
		}
		contextName, owner, name, err := resolveComponentCatalogTarget(cmd, args)
		if err != nil {
			return err
		}
		invocation := []string{"__component", "list"}
		if name != "" {
			invocation = append(invocation, name)
		}
		return invokeComponentPluginCommand(cmd, owner, contextName, invocation)
	},
}

var componentDescribeCmd = &cobra.Command{
	Use:     "describe",
	Aliases: []string{"status"},
	Short:   "Show the current state of each component",
	Long: `Show the current state of each component registered by the active context's plugin.

Each component is reported as on, off, or drifted. A drifted component means the project files no
longer match the last recorded state — run reconcile to bring them back into alignment.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		contextName, owner, name, err := resolveComponentOwner(cmd, componentDescribeName)
		if err != nil {
			return err
		}

		invocation := []string{"__component", "describe"}
		if name != "" {
			invocation = append(invocation, "--component", name)
		}
		if strings.TrimSpace(componentDescribePath) != "" {
			invocation = append(invocation, "--path", componentDescribePath)
		}
		if strings.TrimSpace(componentDescribeDrupalRoot) != "" {
			invocation = append(invocation, "--drupal-rootfs", componentDescribeDrupalRoot)
		}
		if componentDescribeVerbose {
			invocation = append(invocation, "--verbose")
		}
		if strings.TrimSpace(componentDescribeFormat) != "" {
			invocation = append(invocation, "--format", componentDescribeFormat)
		}

		return invokePluginCommand(owner, contextName, invocation)
	},
}

var componentReconcileCmd = &cobra.Command{
	Use:     "reconcile",
	Aliases: []string{"review", "align"},
	Short:   "Detect and repair component configuration drift",
	Long: `Inspect each component and apply any changes needed to bring the project back into alignment.

By default the command is interactive and asks before applying changes. Pass --report to preview
what would change without applying it.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		contextName, owner, name, err := resolveComponentOwner(cmd, componentReconcileName)
		if err != nil {
			return err
		}

		invocation := []string{"__component", "reconcile"}
		if name != "" {
			invocation = append(invocation, "--component", name)
		}
		if strings.TrimSpace(componentReconcilePath) != "" {
			invocation = append(invocation, "--path", componentReconcilePath)
		}
		if strings.TrimSpace(componentReconcileDrupalRoot) != "" {
			invocation = append(invocation, "--drupal-rootfs", componentReconcileDrupalRoot)
		}
		if componentReconcileReport {
			invocation = append(invocation, "--report")
		}
		if componentReconcileVerbose {
			invocation = append(invocation, "--verbose")
		}
		if strings.TrimSpace(componentReconcileFormat) != "" {
			invocation = append(invocation, "--format", componentReconcileFormat)
		}

		return invokePluginCommand(owner, contextName, invocation)
	},
}

var componentSetCmd = &cobra.Command{
	Use:                "set <component> [disposition] [flags]",
	Short:              "Enable, disable, or reconfigure a component",
	DisableFlagParsing: true,
	Long: `Set the state or disposition of a named component in the active context's plugin.

Prefix the component name with the plugin namespace to target it directly:

  sitectl component set isle/fcrepo off
  sitectl component set isle/blazegraph off

All flags after "set" are forwarded to the plugin's component set handler. Use
"sitectl component set --help" or "sitectl component list <component>" to see
component-specific flags.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if containsHelpArg(args) {
			return writeComponentSetHelp(cmd, args)
		}

		contextName, owner, forwardedArgs, err := resolveComponentSetInvocation(cmd, args)
		if err != nil {
			return err
		}

		invocation := append([]string{"__component", "set"}, forwardedArgs...)
		return invokeComponentPluginCommand(cmd, owner, contextName, invocation)
	},
}

func init() {
	pluginSDK = plugin.NewSDK(plugin.Metadata{Name: "sitectl"})

	componentDescribeCmd.Flags().StringVarP(&componentDescribeName, "component", "c", "", "Component to describe, e.g. isle/fcrepo. Defaults to all components.")
	componentDescribeCmd.Flags().StringVar(&componentDescribePath, "path", "", "Path to the project directory. Defaults to the active context project directory.")
	componentDescribeCmd.Flags().StringVar(&componentDescribeDrupalRoot, "drupal-rootfs", "", "Path to the Drupal web root, relative to --path.")
	componentDescribeCmd.Flags().BoolVar(&componentDescribeVerbose, "verbose", false, "Show additional details for each component.")
	componentDescribeCmd.Flags().StringVar(&componentDescribeFormat, "format", "", "Output format (default: table).")

	componentReconcileCmd.Flags().StringVarP(&componentReconcileName, "component", "c", "", "Component to reconcile, e.g. isle/fcrepo. Defaults to all components.")
	componentReconcileCmd.Flags().StringVar(&componentReconcilePath, "path", "", "Path to the project directory. Defaults to the active context project directory.")
	componentReconcileCmd.Flags().StringVar(&componentReconcileDrupalRoot, "drupal-rootfs", "", "Path to the Drupal web root, relative to --path.")
	componentReconcileCmd.Flags().BoolVar(&componentReconcileReport, "report", false, "Preview changes without applying them.")
	componentReconcileCmd.Flags().BoolVar(&componentReconcileVerbose, "verbose", false, "Show additional details for each component.")
	componentReconcileCmd.Flags().StringVar(&componentReconcileFormat, "format", "", "Output format (default: table).")

	componentSetCmd.Flags().StringVar(&componentSetPath, "path", "", "Path to the project directory. Defaults to the active context project directory.")
	componentSetCmd.Flags().StringVar(&componentSetDrupalRoot, "drupal-rootfs", "", "Path to the Drupal web root, relative to --path.")
	componentSetCmd.Flags().StringVar(&componentSetState, "state", "", "State to apply (on, off).")
	componentSetCmd.Flags().StringVar(&componentSetDisposition, "disposition", "", "Disposition to apply (enabled, disabled, superceded, distributed).")
	componentSetCmd.Flags().StringVar(&componentSetTLSMode, "tls-mode", "", "TLS mode (http, self-managed, mkcert, letsencrypt).")
	componentSetCmd.Flags().BoolVar(&componentSetYolo, "yolo", false, "Skip the confirmation prompt.")

	componentCmd.AddCommand(componentListCmd)
	componentCmd.AddCommand(componentDescribeCmd)
	componentCmd.AddCommand(componentReconcileCmd)
	componentCmd.AddCommand(componentSetCmd)
	componentCmd.GroupID = "advanced"
	RootCmd.AddCommand(componentCmd)
}

var pluginSDK *plugin.SDK

func resolveComponentOwner(cmd *cobra.Command, raw string) (string, string, string, error) {
	name := strings.TrimSpace(raw)
	ownerHint := ""
	if pluginName, componentName, ok := splitNamespacedComponent(name); ok {
		ownerHint = pluginName
		name = componentName
	}
	contextName, err := resolveContextNameForPlugin(cmd, ownerHint)
	if err != nil {
		return "", "", "", err
	}

	ctx, err := config.GetContextForPlugin(contextName, ownerHint)
	if err != nil {
		return "", "", "", err
	}

	owner := ctx.Plugin
	if ownerHint != "" {
		owner = ownerHint
	}
	if strings.TrimSpace(owner) == "" {
		return "", "", "", fmt.Errorf("context %q does not define a plugin owner", ctx.Name)
	}
	if owner == "core" {
		return "", "", "", fmt.Errorf("context %q uses plugin %q; component commands require a stack plugin such as isle", ctx.Name, owner)
	}
	return contextName, owner, name, nil
}

func resolveComponentOwnerInContext(contextName, raw string) (string, string, string, error) {
	name := strings.TrimSpace(raw)
	ownerHint := ""
	if pluginName, componentName, ok := splitNamespacedComponent(name); ok {
		ownerHint = pluginName
		name = componentName
	}

	ctx, err := config.GetContextForPlugin(contextName, ownerHint)
	if err != nil {
		return "", "", "", err
	}

	owner := ctx.Plugin
	if ownerHint != "" {
		owner = ownerHint
	}
	if strings.TrimSpace(owner) == "" {
		return "", "", "", fmt.Errorf("context %q does not define a plugin owner", ctx.Name)
	}
	if owner == "core" {
		return "", "", "", fmt.Errorf("context %q uses plugin %q; component commands require a stack plugin such as isle", ctx.Name, owner)
	}
	return contextName, owner, name, nil
}

func resolveComponentSetInvocation(cmd *cobra.Command, args []string) (string, string, []string, error) {
	filteredArgs, contextName, err := getContextFromArgs(cmd, args)
	if err != nil {
		return "", "", nil, err
	}
	componentIndex, rawName, ok := firstComponentSetArg(filteredArgs)
	if !ok {
		return "", "", nil, fmt.Errorf("component name is required")
	}
	contextName, owner, name, err := resolveComponentOwnerInContext(contextName, rawName)
	if err != nil {
		return "", "", nil, err
	}

	forwarded := append([]string{}, filteredArgs...)
	forwarded[componentIndex] = name
	return contextName, owner, forwarded, nil
}

func resolveComponentCatalogTarget(cmd *cobra.Command, args []string) (string, string, string, error) {
	rawArgs, explicitContext, err := rawContextFromArgs(cmd, args)
	if err != nil {
		return "", "", "", err
	}
	rawArgs = stripHelpArgs(rawArgs)
	_, rawName, hasName := firstComponentSetArg(rawArgs)
	if hasName {
		if pluginName, componentName, ok := splitNamespacedComponent(rawName); ok {
			contextName := explicitContext
			if contextName == "" {
				if resolved, resolveErr := resolveContextNameForPluginQuiet(cmd, pluginName); resolveErr == nil {
					contextName = resolved
				}
			}
			return contextName, pluginName, componentName, nil
		}
	}

	contextName := explicitContext
	if contextName == "" {
		contextName, err = resolveContextNameForPluginQuiet(cmd, "")
		if err != nil {
			return "", "", "", err
		}
	}
	ctx, err := config.GetContext(contextName)
	if err != nil {
		return "", "", "", err
	}
	owner := strings.TrimSpace(ctx.Plugin)
	if owner == "" || owner == "core" {
		return "", "", "", fmt.Errorf("context %q does not define a stack plugin", ctx.Name)
	}
	name := ""
	if hasName {
		name = strings.TrimSpace(rawName)
	}
	return contextName, owner, name, nil
}

func rawContextFromArgs(cmd *cobra.Command, args []string) ([]string, string, error) {
	filteredArgs, contextName, err := helpers.GetContextFromArgs(cmd, args)
	if err != nil {
		return nil, "", err
	}
	if contextName != "" {
		resolved, err := resolveExplicitContextName(contextName)
		if err != nil {
			return nil, "", err
		}
		return filteredArgs, resolved, nil
	}
	return filteredArgs, "", nil
}

func resolveContextNameForPluginQuiet(cmd *cobra.Command, pluginName string) (string, error) {
	flags := commandContextFlags(cmd)
	if flags != nil && flags.Lookup("context") != nil && flags.Changed("context") {
		value, err := flags.GetString("context")
		if err != nil {
			return "", fmt.Errorf("error getting context flag: %v", err)
		}
		return resolveExplicitContextName(value)
	}
	contextName, err := config.CurrentForPlugin(pluginName)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(contextName) == "" {
		return "", fmt.Errorf("no current context is set")
	}
	return contextName, nil
}

func firstComponentSetArg(args []string) (int, string, bool) {
	skipNext := false
	afterSeparator := false
	for i, arg := range args {
		if skipNext {
			skipNext = false
			continue
		}
		if afterSeparator {
			if strings.TrimSpace(arg) != "" {
				return i, arg, true
			}
			continue
		}
		if arg == "--" {
			afterSeparator = true
			continue
		}
		if isHelpArg(arg) {
			continue
		}
		if strings.HasPrefix(arg, "--") {
			if componentFlagTakesValue(arg) {
				skipNext = true
			}
			continue
		}
		if strings.HasPrefix(arg, "-") {
			continue
		}
		if strings.TrimSpace(arg) == "" {
			continue
		}
		return i, arg, true
	}
	return -1, "", false
}

func componentFlagTakesValue(arg string) bool {
	trimmed := strings.TrimPrefix(arg, "--")
	if trimmed == "" || strings.Contains(trimmed, "=") {
		return false
	}
	name := trimmed
	if idx := strings.Index(name, "="); idx >= 0 {
		name = name[:idx]
	}
	switch name {
	case "help", "yolo", "verbose", "report":
		return false
	default:
		return true
	}
}

func containsHelpArg(args []string) bool {
	for _, arg := range args {
		if isHelpArg(arg) {
			return true
		}
	}
	return false
}

func isHelpArg(arg string) bool {
	return arg == "-h" || arg == "--help"
}

func stripHelpArgs(args []string) []string {
	out := make([]string, 0, len(args))
	for _, arg := range args {
		if isHelpArg(arg) {
			continue
		}
		out = append(out, arg)
	}
	return out
}

func writeComponentListHelp(cmd *cobra.Command) error {
	_, err := fmt.Fprint(cmd.OutOrStdout(), `List components registered by the active context's plugin.

USAGE

  sitectl component list [component]

EXAMPLES

  sitectl component list
  sitectl component list fcrepo
  sitectl component list isle/fcrepo

`)
	return err
}

func writeComponentSetHelp(cmd *cobra.Command, args []string) error {
	if _, err := fmt.Fprint(cmd.OutOrStdout(), `Set the state or disposition of a named component in the active context's plugin.

USAGE

  sitectl component set <component> [disposition] [flags]

EXAMPLES

  sitectl component set fcrepo superceded --isle-file-system-uri private
  sitectl component set iiif triplet
  sitectl component set iiif-topology distributed --iiif-upstream-url https://iiif.example.org
  sitectl component set isle-tls enabled --tls-mode letsencrypt

COMMON FLAGS

  --context        The sitectl context to use. See sitectl config --help for more info
  --disposition    Disposition to apply. Valid values depend on the component.
  --drupal-rootfs  Path to the Drupal web root, relative to --path.
  -h, --help       Help for set
  --log-level      The logging level for the command
  --path           Path to the project directory. Defaults to the active context project directory.
  --state          State to apply (on, off).
  --tls-mode       TLS mode for TLS-related components.
  --yolo           Skip the confirmation prompt.

COMPONENTS

`); err != nil {
		return err
	}

	contextName, owner, name, err := resolveComponentCatalogTarget(cmd, args)
	if err != nil {
		_, printErr := fmt.Fprintf(cmd.OutOrStdout(), "  Component catalog unavailable: %v\n", err)
		return printErr
	}
	invocation := []string{"__component", "list"}
	if name != "" {
		invocation = append(invocation, name)
	}
	if err := invokeComponentPluginCommand(cmd, owner, contextName, invocation); err != nil {
		_, printErr := fmt.Fprintf(cmd.OutOrStdout(), "  Component catalog unavailable: %v\n", err)
		return printErr
	}
	return nil
}

func splitNamespacedComponent(raw string) (string, string, bool) {
	pluginName, componentName, ok := strings.Cut(strings.TrimSpace(raw), "/")
	if !ok {
		return "", "", false
	}
	pluginName = strings.TrimSpace(pluginName)
	componentName = strings.TrimSpace(componentName)
	if pluginName == "" || componentName == "" {
		return "", "", false
	}
	return pluginName, componentName, true
}

package cmd

import (
	"fmt"
	"strings"

	"github.com/libops/sitectl/pkg/config"
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
	invokePluginCommand          = func(pluginName, contextName string, args []string) error {
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
			Stdin:   RootCmd.InOrStdin(),
			Stdout:  RootCmd.OutOrStdout(),
			Stderr:  RootCmd.ErrOrStderr(),
		})
		return err
	}
)

var componentCmd = &cobra.Command{
	Use:   "component",
	Short: "Inspect and manage stack components for the active context",
	Long: `Components are optional stack features — such as Fcrepo or Blazegraph — that can be toggled on or off.

sitectl dispatches component commands to the plugin associated with the active context. The plugin
provides the component registry; sitectl provides a consistent entry point regardless of which stack
you are working with.`,
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
	Use:   "set <component> [disposition]",
	Short: "Enable, disable, or reconfigure a component",
	Long: `Set the state or disposition of a named component in the active context's plugin.

Prefix the component name with the plugin namespace to target it directly:

  sitectl component set isle/fcrepo off
  sitectl component set isle/blazegraph off`,
	Args: cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		contextName, owner, name, err := resolveComponentOwner(cmd, args[0])
		if err != nil {
			return err
		}

		invocation := []string{"__component", "set", name}
		if len(args) > 1 {
			invocation = append(invocation, args[1])
		}
		if strings.TrimSpace(componentSetPath) != "" {
			invocation = append(invocation, "--path", componentSetPath)
		}
		if strings.TrimSpace(componentSetDrupalRoot) != "" {
			invocation = append(invocation, "--drupal-rootfs", componentSetDrupalRoot)
		}
		if strings.TrimSpace(componentSetState) != "" {
			invocation = append(invocation, "--state", componentSetState)
		}
		if strings.TrimSpace(componentSetDisposition) != "" {
			invocation = append(invocation, "--disposition", componentSetDisposition)
		}
		if strings.TrimSpace(componentSetTLSMode) != "" {
			invocation = append(invocation, "--tls-mode", componentSetTLSMode)
		}
		if componentSetYolo {
			invocation = append(invocation, "--yolo")
		}

		return invokePluginCommand(owner, contextName, invocation)
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

	componentCmd.AddCommand(componentDescribeCmd)
	componentCmd.AddCommand(componentReconcileCmd)
	componentCmd.AddCommand(componentSetCmd)
	componentCmd.GroupID = "advanced"
	RootCmd.AddCommand(componentCmd)
}

var pluginSDK *plugin.SDK

func resolveComponentOwner(cmd *cobra.Command, raw string) (string, string, string, error) {
	contextName, err := config.ResolveCurrentContextName(cmd.Flags())
	if err != nil {
		return "", "", "", err
	}

	ctx, err := config.GetContext(contextName)
	if err != nil {
		return "", "", "", err
	}

	owner := ctx.Plugin
	name := strings.TrimSpace(raw)
	if pluginName, componentName, ok := splitNamespacedComponent(name); ok {
		owner = pluginName
		name = componentName
	}
	if strings.TrimSpace(owner) == "" {
		return "", "", "", fmt.Errorf("context %q does not define a plugin owner", ctx.Name)
	}
	if owner == "core" {
		return "", "", "", fmt.Errorf("context %q uses plugin %q; component commands require a stack plugin such as isle", ctx.Name, owner)
	}
	return contextName, owner, name, nil
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

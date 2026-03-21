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
	invokePluginCommand          = func(pluginName string, args []string) error {
		installed, ok := plugin.FindInstalled(pluginName)
		if !ok {
			return fmt.Errorf("plugin %q is not installed", pluginName)
		}
		_, err := pluginSDK.InvokePluginCommand(installed.Name, args, plugin.CommandExecOptions{
			Stdin:  RootCmd.InOrStdin(),
			Stdout: RootCmd.OutOrStdout(),
			Stderr: RootCmd.ErrOrStderr(),
		})
		return err
	}
)

var componentCmd = &cobra.Command{
	Use:   "component",
	Short: "Describe and reconcile stack components for the active context",
}

var componentDescribeCmd = &cobra.Command{
	Use:     "describe",
	Aliases: []string{"status"},
	Short:   "Describe the current component state",
	RunE: func(cmd *cobra.Command, args []string) error {
		owner, name, err := resolveComponentOwner(cmd, componentDescribeName)
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

		return invokePluginCommand(owner, invocation)
	},
}

var componentReconcileCmd = &cobra.Command{
	Use:     "reconcile",
	Aliases: []string{"review", "align"},
	Short:   "Review and reconcile component state",
	RunE: func(cmd *cobra.Command, args []string) error {
		owner, name, err := resolveComponentOwner(cmd, componentReconcileName)
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

		return invokePluginCommand(owner, invocation)
	},
}

var componentSetCmd = &cobra.Command{
	Use:   "set <component> [disposition]",
	Short: "Set a component disposition",
	Args:  cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		owner, name, err := resolveComponentOwner(cmd, args[0])
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

		return invokePluginCommand(owner, invocation)
	},
}

func init() {
	pluginSDK = plugin.NewSDK(plugin.Metadata{Name: "sitectl"})

	componentDescribeCmd.Flags().StringVarP(&componentDescribeName, "component", "c", "", "Namespaced component to describe, for example isle/fcrepo")
	componentDescribeCmd.Flags().StringVar(&componentDescribePath, "path", "", "Project path override")
	componentDescribeCmd.Flags().StringVar(&componentDescribeDrupalRoot, "drupal-rootfs", "", "Drupal rootfs path override")
	componentDescribeCmd.Flags().BoolVar(&componentDescribeVerbose, "verbose", false, "Include verbose component details")
	componentDescribeCmd.Flags().StringVar(&componentDescribeFormat, "format", "", "Output format override")

	componentReconcileCmd.Flags().StringVarP(&componentReconcileName, "component", "c", "", "Namespaced component to reconcile, for example isle/fcrepo")
	componentReconcileCmd.Flags().StringVar(&componentReconcilePath, "path", "", "Project path override")
	componentReconcileCmd.Flags().StringVar(&componentReconcileDrupalRoot, "drupal-rootfs", "", "Drupal rootfs path override")
	componentReconcileCmd.Flags().BoolVar(&componentReconcileReport, "report", false, "Render a report instead of applying changes")
	componentReconcileCmd.Flags().BoolVar(&componentReconcileVerbose, "verbose", false, "Include verbose component details")
	componentReconcileCmd.Flags().StringVar(&componentReconcileFormat, "format", "", "Output format override")

	componentSetCmd.Flags().StringVar(&componentSetPath, "path", "", "Project path override")
	componentSetCmd.Flags().StringVar(&componentSetDrupalRoot, "drupal-rootfs", "", "Drupal rootfs path override")
	componentSetCmd.Flags().StringVar(&componentSetState, "state", "", "Explicit state override")
	componentSetCmd.Flags().StringVar(&componentSetDisposition, "disposition", "", "Explicit disposition override")
	componentSetCmd.Flags().StringVar(&componentSetTLSMode, "tls-mode", "", "TLS mode override")
	componentSetCmd.Flags().BoolVar(&componentSetYolo, "yolo", false, "Apply without confirmation")

	componentCmd.AddCommand(componentDescribeCmd)
	componentCmd.AddCommand(componentReconcileCmd)
	componentCmd.AddCommand(componentSetCmd)
	RootCmd.AddCommand(componentCmd)
}

var pluginSDK *plugin.SDK

func resolveComponentOwner(cmd *cobra.Command, raw string) (string, string, error) {
	contextName, err := config.ResolveCurrentContextName(cmd.Flags())
	if err != nil {
		return "", "", err
	}

	ctx, err := config.GetContext(contextName)
	if err != nil {
		return "", "", err
	}

	owner := ctx.Plugin
	name := strings.TrimSpace(raw)
	if pluginName, componentName, ok := splitNamespacedComponent(name); ok {
		owner = pluginName
		name = componentName
	}
	if strings.TrimSpace(owner) == "" {
		return "", "", fmt.Errorf("context %q does not define a plugin owner", ctx.Name)
	}
	if owner == "core" {
		return "", "", fmt.Errorf("context %q uses plugin %q; component commands require a stack plugin such as isle", ctx.Name, owner)
	}
	return owner, name, nil
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

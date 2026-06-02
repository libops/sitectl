package cmd

import (
	"fmt"
	"strings"

	"github.com/libops/sitectl/pkg/config"
	"github.com/libops/sitectl/pkg/helpers"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

func getContextFromArgs(cmd *cobra.Command, args []string) ([]string, string, error) {
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
	contextName, err = resolveContextName(cmd)
	if err != nil {
		return nil, "", err
	}
	return filteredArgs, contextName, nil
}

func resolveContextName(cmd *cobra.Command) (string, error) {
	return resolveContextNameForPlugin(cmd, "")
}

func resolveContextNameForPlugin(cmd *cobra.Command, pluginName string) (string, error) {
	return config.ResolveCurrentContextNameForPlugin(commandContextFlags(cmd), pluginName)
}

func resolveCurrentContext(cmd *cobra.Command) (*config.Context, error) {
	contextName, err := resolveContextName(cmd)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(contextName) == "" {
		return nil, fmt.Errorf("no current context is set")
	}
	ctx, err := config.GetContext(contextName)
	if err != nil {
		return nil, err
	}
	return &ctx, nil
}

func commandContextFlags(cmd *cobra.Command) *pflag.FlagSet {
	if cmd == nil {
		return nil
	}
	if root := cmd.Root(); root != nil {
		if flags := root.PersistentFlags(); flags.Lookup("context") != nil {
			return flags
		}
	}
	if flags := cmd.Flags(); flags.Lookup("context") != nil {
		return flags
	}
	if flags := cmd.InheritedFlags(); flags.Lookup("context") != nil {
		return flags
	}
	return cmd.Flags()
}

func resolveExplicitContextName(value string) (string, error) {
	value = strings.Trim(value, `" `)
	if value != "default" {
		return value, nil
	}
	cfg, err := config.Load()
	if err != nil {
		return "", err
	}
	return cfg.CurrentContext, nil
}

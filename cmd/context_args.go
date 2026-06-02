package cmd

import (
	"github.com/libops/sitectl/pkg/config"
	"github.com/libops/sitectl/pkg/helpers"
	"github.com/spf13/cobra"
)

func getContextFromArgs(cmd *cobra.Command, args []string) ([]string, string, error) {
	filteredArgs, contextName, err := helpers.GetContextFromArgs(cmd, args)
	if err != nil {
		return nil, "", err
	}
	if contextName != "" {
		return filteredArgs, contextName, nil
	}
	contextName, err = config.ResolveCurrentContextName(cmd.Root().PersistentFlags())
	if err != nil {
		return nil, "", err
	}
	return filteredArgs, contextName, nil
}

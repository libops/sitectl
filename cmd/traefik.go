package cmd

import "github.com/spf13/cobra"

func traefikCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "traefik",
		Short:   "Operate on the Traefik service in the active context",
		GroupID: "ops",
	}
	cmd.AddCommand(serviceStatusCommand("traefik"))
	return cmd
}

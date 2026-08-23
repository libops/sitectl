//go:build !linux

package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

func hostCommand() *cobra.Command {
	return &cobra.Command{
		Use:     "host",
		Short:   "Provision and operate a sitectl-managed Compose host",
		GroupID: "advanced",
		Long: `Provision and operate a Linux VM using sitectl's managed-host layout.

Host operations execute on the target Linux machine. From macOS or Windows,
connect to that machine first and invoke sitectl there, for example with SSH.`,
		Example: `  ssh root@compose.example.org sitectl host diagnostics status`,
		Args:    cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return fmt.Errorf("sitectl host operations require Linux and must run on the target host")
		},
	}
}

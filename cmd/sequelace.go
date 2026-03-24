package cmd

import (
	"fmt"
	"log/slog"
	"os/exec"
	"runtime"

	"github.com/libops/sitectl/pkg/config"
	"github.com/libops/sitectl/pkg/docker"

	"github.com/spf13/cobra"
)

var sequelAceCmd = &cobra.Command{
	Use:   "sequelace",
	Short: "Open the site database in Sequel Ace (macOS only)",
	Long: `Open a direct connection to the site's MySQL/MariaDB container in Sequel Ace.

For remote contexts, sitectl establishes an SSH tunnel before launching Sequel Ace so the
database port is never exposed on the host. This command is macOS only.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if runtime.GOOS != "darwin" {
			return fmt.Errorf("sequelace is only supported on mac OS")
		}

		f := cmd.Flags()
		context, err := config.CurrentContext(f)
		if err != nil {
			return err
		}

		sequelAcePath, err := f.GetString("sequel-ace-path")
		if err != nil {
			return err
		}

		mysql, ssh, err := docker.GetDatabaseUris(context)
		if err != nil {
			return err
		}
		slog.Debug("uris", "mysql", mysql, "ssh", ssh)
		cmdArgs := []string{
			"-a",
			sequelAcePath,
			fmt.Sprintf("%s?%s", mysql, ssh),
		}
		openCmd := exec.Command("open", cmdArgs...)
		if err := openCmd.Run(); err != nil {
			slog.Error("Could not open sequelace.")
			return err
		}

		return nil
	},
}

func init() {
	RootCmd.AddCommand(sequelAceCmd)

	sequelAceCmd.Flags().String("sequel-ace-path", "/Applications/Sequel Ace.app/Contents/MacOS/Sequel Ace", "Path to the Sequel Ace binary.")
}

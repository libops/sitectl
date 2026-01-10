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
	Short: "Connect to your MySQL/Mariadb database using Sequel Ace (Mac OS only)",
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
			fmt.Sprintf("%s?%s", mysql, ssh),
			"-a",
			sequelAcePath,
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

	sequelAceCmd.Flags().String("sequel-ace-path", "/Applications/Sequel Ace.app/Contents/MacOS/Sequel Ace", "Full path to your Sequel Ace app")
}

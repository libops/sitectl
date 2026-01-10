package cmd

import (
	"os/exec"

	"github.com/libops/sitectl/pkg/config"
	"github.com/libops/sitectl/pkg/helpers"
	"github.com/spf13/cobra"
)

// makeCmd support deprecated custom make commands
var makeCmd = &cobra.Command{
	Use:   "make",
	Short: "Run custom make commands",
	Args:  cobra.ArbitraryArgs,
	Run: func(cmd *cobra.Command, args []string) {
		f := cmd.Flags()
		context, err := config.CurrentContext(f)
		if err != nil {
			helpers.ExitOnError(err)
		}

		c := exec.Command("make", args...)
		c.Dir = context.ProjectDir
		_, err = context.RunCommand(c)
		if err != nil {
			helpers.ExitOnError(err)
		}
	},
}

func init() {
	RootCmd.AddCommand(makeCmd)
}

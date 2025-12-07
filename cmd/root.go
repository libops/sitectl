package cmd

import (
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/libops/sitectl/pkg/config"
	"github.com/spf13/cobra"
)

var RootCmd = &cobra.Command{
	Use:   "sitectl",
	Short: "Interact with your docker compose site",
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		level := slog.LevelInfo
		ll, err := cmd.Flags().GetString("log-level")
		if err != nil {
			return err
		}

		switch strings.ToUpper(ll) {
		case "DEBUG":
			level = slog.LevelDebug
		case "WARN":
			level = slog.LevelWarn
		case "ERROR":
			level = slog.LevelError
		}

		opts := &slog.HandlerOptions{
			Level: level,
		}
		handler := slog.New(slog.NewTextHandler(os.Stdout, opts))
		slog.SetDefault(handler)

		return nil
	},
}

func Execute() {
	err := RootCmd.Execute()
	if err != nil {
		os.Exit(1)
	}
}

func SetVersionInfo(version, commit, date string) {
	RootCmd.Version = fmt.Sprintf("%s (Built on %s from Git SHA %s)", version, date, commit)
}

func init() {
	c, err := config.Current()
	if err != nil {
		slog.Error("Unable to fetch current context", "err", err)
	}

	ll := os.Getenv("LOG_LEVEL")
	if ll == "" {
		ll = "INFO"
	}

	apiURL := os.Getenv("LIBOPS_API_URL")
	if apiURL == "" {
		apiURL = "https://api.libops.io"
	}

	RootCmd.PersistentFlags().String("context", c, "The sitectl context to use. See sitectl config --help for more info")
	RootCmd.PersistentFlags().String("log-level", ll, "The logging level for the command")
	RootCmd.PersistentFlags().String("api-url", apiURL, "Base URL of the libops API")
	RootCmd.PersistentFlags().String("format", "table", `Format output using a custom template:
  'table':            Print output in table format with column headers (default)
  'table TEMPLATE':   Print output in table format using the given Go template
  'json':             Print in JSON format
  'TEMPLATE':         Print output using the given Go template`)
}

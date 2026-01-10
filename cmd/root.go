package cmd

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

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

	RootCmd.PersistentFlags().String("context", c, "The sitectl context to use. See sitectl config --help for more info")
	RootCmd.PersistentFlags().String("log-level", ll, "The logging level for the command")
	discoverAndRegisterPlugins()
}

func discoverAndRegisterPlugins() {
	path := os.Getenv("PATH")
	paths := strings.SplitSeq(path, string(os.PathListSeparator))

	for p := range paths {
		files, err := os.ReadDir(p)
		if err != nil {
			continue // Ignore directories that can't be read
		}

		for _, f := range files {
			if !f.IsDir() && strings.HasPrefix(f.Name(), "sitectl-") && f.Name() != "sitectl" {
				pluginName := strings.TrimPrefix(f.Name(), "sitectl-")
				pluginPath := filepath.Join(p, f.Name())

				// Try to get plugin description from metadata
				description := getPluginDescription(pluginPath, pluginName)

				pluginCmd := &cobra.Command{
					Use:   pluginName,
					Short: description,
					RunE: func(cmd *cobra.Command, args []string) error {
						err := syscall.Exec(pluginPath, append([]string{f.Name()}, args...), os.Environ())
						if err != nil {
							return fmt.Errorf("failed to execute plugin %q: %w", pluginName, err)
						}
						return nil
					},
					DisableFlagParsing: true,
				}
				RootCmd.AddCommand(pluginCmd)
			}
		}
	}
}

// getPluginDescription attempts to fetch plugin metadata
func getPluginDescription(pluginPath, pluginName string) string {
	// Try to execute plugin with plugin-info command to get description
	cmd := exec.Command(pluginPath, "plugin-info")
	_, err := cmd.Output()

	// If we can't get metadata, use a default description
	if err != nil {
		return fmt.Sprintf("the %s plugin", pluginName)
	}

	// For now, return default - a more complete implementation would parse the output
	return fmt.Sprintf("the %s plugin", pluginName)
}

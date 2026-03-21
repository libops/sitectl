package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"charm.land/fang/v2"
	"github.com/libops/sitectl/pkg/config"
	"github.com/libops/sitectl/pkg/plugin"
	"github.com/libops/sitectl/pkg/tui"
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
	RunE: func(cmd *cobra.Command, args []string) error {
		return tui.Run()
	},
}

func Execute() {
	runCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err := fang.Execute(
		runCtx,
		RootCmd,
		fang.WithVersion(RootCmd.Version),
	)
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
	for _, discovered := range plugin.DiscoverInstalled() {
		pluginName := discovered.Name
		pluginPath := discovered.Path
		binaryName := discovered.BinaryName
		description := discovered.Description

		pluginCmd := &cobra.Command{
			Use:   pluginName,
			Short: description,
			RunE: func(cmd *cobra.Command, args []string) error {
				err := syscall.Exec(pluginPath, append([]string{binaryName}, args...), os.Environ())
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

package plugin

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/libops/sitectl/pkg/config"
	"github.com/libops/sitectl/pkg/plugin/debugui"
	"github.com/spf13/cobra"
)

// DebugRunner renders plugin-specific debug diagnostics.
// Render returns the body content (without panel wrapper) for the plugin's debug section.
// The SDK wraps the body in a named panel and delegates to included plugins.
type DebugRunner interface {
	BindFlags(cmd *cobra.Command)
	Render(cmd *cobra.Command, ctx *config.Context) (string, error)
}

// RegisterDebugRunner registers a debug runner for the plugin. BindFlags must
// declare the RPC-bridged --verbose flag when the runner accepts core verbose
// state; registration panics if required bridge flags are missing.
// The SDK stores the debug handler, wraps the runner's output in a named panel,
// and delegates to any included plugins through the RPC entrypoint.
// Render implementations should return their text to the SDK; lower-level
// command handlers must write with cmd.OutOrStdout(), not fmt.Println.
func (s *SDK) RegisterDebugRunner(runner DebugRunner) {
	if s == nil || runner == nil {
		return
	}
	cmd := &cobra.Command{
		Use:          "debug",
		Short:        "Internal debug extension command",
		Hidden:       true,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := s.GetContext()
			if err != nil {
				return err
			}
			body, err := runner.Render(cmd, ctx)
			if err != nil {
				return err
			}
			rendered := debugui.RenderPanel(s.Metadata.Name, body)

			// Delegate to included plugins.
			for _, include := range s.Metadata.Includes {
				slog.Debug("running included plugin debug", "plugin", s.Metadata.Name, "include", include)
				resp, invokeErr := s.InvokeIncludedPluginRPC(include, NewRPCRequest(MethodDebugRun), CommandExecOptions{
					Context: cmd.Context(),
				})
				if invokeErr != nil {
					rendered += "\n\n" + debugui.RenderPanel(include, debugui.FormatRows([]debugui.Row{
						{Label: "Status", Value: debugui.Status("warning")},
						{Label: "Detail", Value: invokeErr.Error()},
					}))
					continue
				}
				if trimmed := strings.TrimSpace(resp.Output); trimmed != "" {
					slog.Debug("included plugin debug completed", "plugin", s.Metadata.Name, "include", include)
					rendered += "\n\n" + trimmed
				} else {
					slog.Debug("included plugin returned empty debug output", "plugin", s.Metadata.Name, "include", include)
				}
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), rendered)
			return err
		},
	}
	runner.BindFlags(cmd)
	s.registerDebugCommand(cmd)
	s.hasDebug = true
}

// RegisterDebugHandler registers a debug runner for the plugin.
//
// Deprecated: use RegisterDebugRunner.
func (s *SDK) RegisterDebugHandler(runner DebugRunner) {
	s.RegisterDebugRunner(runner)
}

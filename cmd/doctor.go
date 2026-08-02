package cmd

import (
	"fmt"
	"strings"

	"github.com/libops/sitectl/pkg/plugin"
	"github.com/libops/sitectl/pkg/validate"
	"github.com/spf13/cobra"
)

func init() { RootCmd.AddCommand(doctorCommand()) }

func doctorCommand() *cobra.Command {
	return &cobra.Command{
		Use: "doctor", Short: "Diagnose unhealthy site services and print next actions", GroupID: "troubleshoot", Args: cobra.NoArgs,
		Long: "Inspect Compose and application health for the active site, then turn common failure states into concrete operator actions.",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := resolveCurrentContext(cmd)
			if err != nil {
				return err
			}
			results, err := runHealthcheckOnce(cmd, ctx, ctx.Name, plugin.HealthcheckRunParams{}, nil)
			if err != nil {
				return err
			}
			valid := true
			for _, result := range results {
				status := string(result.Status)
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s: %s", result.Name, status)
				if strings.TrimSpace(result.Detail) != "" {
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), " — %s", result.Detail)
				}
				_, _ = fmt.Fprintln(cmd.OutOrStdout())
				if result.Status == validate.StatusFailed {
					valid = false
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  next: %s\n", doctorHint(result.Detail))
				}
			}
			if !valid {
				return fmt.Errorf("doctor found unhealthy site services")
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), "No unhealthy services found.")
			return err
		},
	}
}

func doctorHint(detail string) string {
	value := strings.ToLower(detail)
	switch {
	case strings.Contains(value, "health=starting"):
		return "wait for startup, then run `sitectl doctor` again; inspect logs if the state does not advance"
	case strings.Contains(value, "unhealthy"):
		return "run `sitectl debug` and inspect the named service's healthcheck and recent logs"
	case strings.Contains(value, "missing") || strings.Contains(value, "not found"):
		return "run `sitectl compose reconcile` to restore declared init state and services"
	case strings.Contains(value, "exited"):
		return "run `sitectl debug`, fix the first failing service, then run `sitectl compose reconcile`"
	default:
		return "run `sitectl debug` for service logs and resource diagnostics"
	}
}

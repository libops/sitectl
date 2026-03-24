package cmd

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/libops/sitectl/pkg/config"
	corecron "github.com/libops/sitectl/pkg/cron"
	"github.com/libops/sitectl/pkg/plugin"
	"github.com/spf13/cobra"
	yaml "gopkg.in/yaml.v3"
)

var (
	cronSpecContext              string
	cronSpecSchedule             string
	cronSpecOutputDir            string
	cronSpecComponents           []string
	cronSpecRetentionDays        int
	cronSpecPreserveFirstOfMonth bool
	cronSpecDockerPrune          bool
)

var cronCmd = &cobra.Command{
	Use:   "cron",
	Short: "Manage scheduled cron jobs",
	Long: `Cron specs define scheduled jobs for a sitectl context. Each spec records which context
to run on, what schedule to use, which job components to run, and where to store output.

Use render-systemd to turn a spec into systemd units you can install on the host.`,
}

var cronAddCmd = &cobra.Command{
	Use:   "add NAME",
	Args:  cobra.ExactArgs(1),
	Short: "Create or update a cron spec",
	Long: `Create or update a cron spec with the given name.

A cron spec stores the schedule, target context, job components, output directory, and
retention policy for a scheduled job. Once saved, use render-systemd to generate the
systemd units and install them on the host.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		name := strings.TrimSpace(args[0])
		if name == "" {
			return fmt.Errorf("cron spec name is required")
		}
		if strings.TrimSpace(cronSpecContext) == "" {
			return fmt.Errorf("--cron-context is required")
		}
		if strings.TrimSpace(cronSpecSchedule) == "" {
			return fmt.Errorf("--schedule is required")
		}
		if strings.TrimSpace(cronSpecOutputDir) == "" {
			return fmt.Errorf("--output-dir is required")
		}

		ctx, err := config.GetContext(cronSpecContext)
		if err != nil {
			return fmt.Errorf("load context %q: %w", cronSpecContext, err)
		}
		components, err := normalizeCronComponents(ctx, cronSpecComponents)
		if err != nil {
			return err
		}

		spec := config.CronSpec{
			Name:                 name,
			Context:              ctx.Name,
			Schedule:             cronSpecSchedule,
			OutputDir:            cronSpecOutputDir,
			Components:           components,
			RetentionDays:        cronSpecRetentionDays,
			PreserveFirstOfMonth: cronSpecPreserveFirstOfMonth,
			DockerPrune:          cronSpecDockerPrune,
		}
		if spec.RetentionDays < 0 {
			return fmt.Errorf("--retention-days must be >= 0")
		}
		if err := config.SaveCronSpec(spec); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Saved cron spec %q for context %q\n", spec.Name, spec.Context)
		return nil
	},
}

var cronListCmd = &cobra.Command{
	Use:   "list",
	Short: "List configured cron specs",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		if len(cfg.CronSpecs) == 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "No cron specs configured")
			return nil
		}

		w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tCONTEXT\tSCHEDULE\tOUTPUT\tCOMPONENTS\tRETENTION")
		for _, spec := range cfg.CronSpecs {
			retention := "-"
			if spec.RetentionDays > 0 {
				retention = strconv.Itoa(spec.RetentionDays) + "d"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
				spec.Name,
				spec.Context,
				spec.Schedule,
				spec.OutputDir,
				strings.Join(spec.Components, ", "),
				retention,
			)
		}
		return w.Flush()
	},
}

var cronDeleteCmd = &cobra.Command{
	Use:   "delete NAME",
	Args:  cobra.ExactArgs(1),
	Short: "Delete a cron spec",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := config.DeleteCronSpec(args[0]); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Deleted cron spec %q\n", args[0])
		return nil
	},
}

var cronComponentsCmd = &cobra.Command{
	Use:   "components",
	Short: "List available cron components for the active or selected context",
	RunE: func(cmd *cobra.Command, args []string) error {
		contextName, err := config.ResolveCurrentContextName(cmd.Flags())
		if err != nil {
			return err
		}
		ctx, err := config.GetContext(contextName)
		if err != nil {
			return err
		}
		specs, err := availableCronComponents(ctx)
		if err != nil {
			return err
		}
		if len(specs) == 0 {
			fmt.Fprintf(cmd.OutOrStdout(), "No cron components available for context %q\n", ctx.Name)
			return nil
		}
		w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tPLUGIN\tFILENAME\tDESCRIPTION")
		for _, spec := range specs {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", spec.Name, spec.Plugin, spec.Filename, spec.Description)
		}
		return w.Flush()
	},
}

var cronRunCmd = &cobra.Command{
	Use:   "run NAME",
	Args:  cobra.ExactArgs(1),
	Short: "Run a configured cron spec now",
	RunE: func(cmd *cobra.Command, args []string) error {
		spec, err := config.GetCronSpec(args[0])
		if err != nil {
			return err
		}
		ctx, err := config.GetContext(spec.Context)
		if err != nil {
			return fmt.Errorf("load context %q: %w", spec.Context, err)
		}
		return runCronSpec(cmd, spec, ctx)
	},
}

var cronRenderSystemdCmd = &cobra.Command{
	Use:   "render-systemd NAME",
	Args:  cobra.ExactArgs(1),
	Short: "Print systemd unit files and install instructions for a cron spec",
	Long: `Print the systemd .service and .timer unit files for the named cron spec, along with
step-by-step instructions for installing them on the target host.

For remote contexts, copy the units to the host manually and install them there.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		spec, err := config.GetCronSpec(args[0])
		if err != nil {
			return err
		}
		ctx, err := config.GetContext(spec.Context)
		if err != nil {
			return fmt.Errorf("load context %q: %w", spec.Context, err)
		}

		serviceName, timerName := cronUnitNames(spec.Name)
		serviceUnit, timerUnit, err := renderCronSystemdUnits(spec)
		if err != nil {
			return err
		}
		fmt.Fprint(cmd.OutOrStdout(), renderCronSystemdInstructions(spec, ctx, serviceName, timerName, serviceUnit, timerUnit))
		return nil
	},
}

var cronInstalledCmd = &cobra.Command{
	Use:   "installed",
	Short: "List installed sitectl systemd cron units on a context host",
	RunE: func(cmd *cobra.Command, args []string) error {
		contextName, err := config.ResolveCurrentContextName(cmd.Flags())
		if err != nil {
			return err
		}
		ctx, err := config.GetContext(contextName)
		if err != nil {
			return err
		}
		units, err := listInstalledCronUnits(&ctx)
		if err != nil {
			return err
		}
		if len(units) == 0 {
			fmt.Fprintf(cmd.OutOrStdout(), "No sitectl systemd cron units found on context %q\n", ctx.Name)
			return nil
		}
		for _, unit := range units {
			fmt.Fprintln(cmd.OutOrStdout(), unit)
		}
		return nil
	},
}

func runCronSpec(cmd *cobra.Command, spec config.CronSpec, ctx config.Context) error {
	available, err := availableCronComponents(ctx)
	if err != nil {
		return err
	}
	selected, err := selectCronComponents(spec.Components, available)
	if err != nil {
		return err
	}

	now := time.Now().UTC()
	outDir, err := corecron.EnsureDatedDestination(spec.OutputDir, now)
	if err != nil {
		return err
	}

	for _, component := range selected {
		outputPath := filepath.Join(outDir, component.Filename)
		if err := runCronComponent(cmd, ctx, component, outputPath); err != nil {
			return fmt.Errorf("run cron component %q: %w", component.Name, err)
		}
	}

	if err := corecron.PruneArtifacts(spec.OutputDir, now, spec.RetentionDays, spec.PreserveFirstOfMonth); err != nil {
		return err
	}
	if spec.DockerPrune {
		if _, err := ctx.RunQuietCommandContext(cmd.Context(), exec.Command("docker", "system", "prune", "-af")); err != nil {
			return fmt.Errorf("docker system prune: %w", err)
		}
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Cron job %q wrote artifacts under %s\n", spec.Name, outDir)
	return nil
}

func runCronComponent(cmd *cobra.Command, ctx config.Context, component corecron.ComponentSpec, outputPath string) error {
	_, err := pluginSDK.InvokePluginCommand(component.Plugin, []string{
		"--context", ctx.Name,
		"__cron", "run",
		"--component", component.Name,
		"--output", outputPath,
	}, plugin.CommandExecOptions{
		Context: RootCmd.Context(),
		Stdin:   RootCmd.InOrStdin(),
		Stdout:  RootCmd.OutOrStdout(),
		Stderr:  RootCmd.ErrOrStderr(),
	})
	return err
}

func renderCronSystemdUnits(spec config.CronSpec) (string, string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", "", fmt.Errorf("resolve sitectl path: %w", err)
	}

	execLine := strings.Join([]string{exe, "--context", spec.Context, "cron", "run", spec.Name}, " ")
	serviceName, _ := cronUnitNames(spec.Name)
	var service bytes.Buffer
	fmt.Fprintf(&service, "[Unit]\nDescription=Sitectl cron job for %s\nAfter=network-online.target\nWants=network-online.target\n\n", spec.Name)
	fmt.Fprintf(&service, "[Service]\nType=oneshot\nExecStart=%s\n", execLine)

	var timer bytes.Buffer
	fmt.Fprintf(&timer, "[Unit]\nDescription=Schedule sitectl cron job for %s\n\n", spec.Name)
	fmt.Fprintf(&timer, "[Timer]\nOnCalendar=%s\nPersistent=true\nUnit=%s\n\n", spec.Schedule, serviceName)
	fmt.Fprintf(&timer, "[Install]\nWantedBy=timers.target\n")

	return service.String(), timer.String(), nil
}

func renderCronSystemdInstructions(spec config.CronSpec, ctx config.Context, serviceName, timerName, serviceUnit, timerUnit string) string {
	var out bytes.Buffer
	fmt.Fprintf(&out, "Cron job: %s\nContext: %s\nHost type: %s\n\n", spec.Name, ctx.Name, ctx.DockerHostType)
	fmt.Fprintln(&out, "Prerequisites:")
	fmt.Fprintln(&out, "- `sitectl` must be installed on the host and available on `$PATH`.")
	fmt.Fprintln(&out, "- The host must be able to run the selected context.")
	fmt.Fprintln(&out)
	if ctx.DockerHostType == config.ContextLocal {
		fmt.Fprintln(&out, "Install on the local host with:")
	} else {
		fmt.Fprintln(&out, "Remote manual setup is required for now.")
		fmt.Fprintln(&out, "Until sitectl supports remote sudo actions, SSH to the target host and install these units there manually.")
		fmt.Fprintln(&out, "Tracking: https://github.com/libops/sitectl/issues")
		fmt.Fprintln(&out)
		fmt.Fprintln(&out, "Run on the remote host with:")
	}
	fmt.Fprintf(&out, "sudo tee /etc/systemd/system/%s >/dev/null <<'EOF'\n%sEOF\n\n", serviceName, serviceUnit)
	fmt.Fprintf(&out, "sudo tee /etc/systemd/system/%s >/dev/null <<'EOF'\n%sEOF\n\n", timerName, timerUnit)
	fmt.Fprintf(&out, "sudo systemctl daemon-reload\nsudo systemctl enable --now %s\n", timerName)
	return out.String()
}

func cronUnitNames(name string) (string, string) {
	base := "sitectl-" + strings.TrimSpace(name)
	return base + ".service", base + ".timer"
}

func listInstalledCronUnits(ctx *config.Context) ([]string, error) {
	accessor, err := config.NewFileAccessor(ctx)
	if err != nil {
		return nil, err
	}
	defer accessor.Close()

	files, err := accessor.ListFiles("/etc/systemd/system")
	if err != nil {
		return nil, err
	}
	units := make([]string, 0, len(files))
	seen := map[string]bool{}
	for _, rel := range files {
		name := filepath.Base(rel)
		if !strings.HasPrefix(name, "sitectl-") {
			continue
		}
		if !strings.HasSuffix(name, ".service") && !strings.HasSuffix(name, ".timer") {
			continue
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		units = append(units, name)
	}
	sort.Strings(units)
	return units, nil
}

func normalizeCronComponents(ctx config.Context, requested []string) ([]string, error) {
	available, err := availableCronComponents(ctx)
	if err != nil {
		return nil, err
	}
	if len(requested) == 0 {
		names := make([]string, 0, len(available))
		for _, spec := range available {
			names = append(names, spec.Name)
		}
		return names, nil
	}
	selected, err := selectCronComponents(requested, available)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(selected))
	for _, spec := range selected {
		names = append(names, spec.Name)
	}
	return names, nil
}

func selectCronComponents(names []string, available []corecron.ComponentSpec) ([]corecron.ComponentSpec, error) {
	selected := make([]corecron.ComponentSpec, 0, len(names))
	seen := map[string]bool{}
	for _, name := range names {
		spec, ok := corecron.FindComponent(available, name)
		if !ok {
			return nil, fmt.Errorf("unknown cron component %q", name)
		}
		if seen[spec.Name] {
			continue
		}
		seen[spec.Name] = true
		selected = append(selected, spec)
	}
	return selected, nil
}

func availableCronComponents(ctx config.Context) ([]corecron.ComponentSpec, error) {
	owners := cronPluginsForContext(ctx.Plugin)
	var combined []corecron.ComponentSpec
	for _, owner := range owners {
		output, err := pluginSDK.InvokePluginCommand(owner, []string{"--context", ctx.Name, "__cron", "components"}, plugin.CommandExecOptions{
			Context: RootCmd.Context(),
			Capture: true,
		})
		if err != nil {
			continue
		}
		var specs []corecron.ComponentSpec
		if err := yaml.Unmarshal([]byte(output), &specs); err != nil {
			return nil, fmt.Errorf("parse cron components from plugin %q: %w", owner, err)
		}
		for i := range specs {
			if strings.TrimSpace(specs[i].Plugin) == "" {
				specs[i].Plugin = owner
			}
		}
		if err := corecron.ValidateComponents(specs); err != nil {
			return nil, fmt.Errorf("validate cron components from plugin %q: %w", owner, err)
		}
		combined = append(combined, specs...)
	}
	corecron.SortComponents(combined)
	return combined, nil
}

func cronPluginsForContext(root string) []string {
	if strings.TrimSpace(root) == "" {
		return nil
	}
	seen := map[string]bool{}
	var ordered []string
	var walk func(string)
	walk = func(name string) {
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		ordered = append(ordered, name)
		installed, ok := plugin.FindInstalled(name)
		if !ok {
			return
		}
		for _, include := range installed.Includes {
			walk(include)
		}
	}
	walk(root)
	return ordered
}

func init() {
	cronAddCmd.Flags().StringVar(&cronSpecContext, "cron-context", "", "sitectl context this cron spec will execute against.")
	cronAddCmd.Flags().StringVar(&cronSpecSchedule, "schedule", "", "systemd OnCalendar expression, e.g. daily or *-*-* 03:00:00.")
	cronAddCmd.Flags().StringVar(&cronSpecOutputDir, "output-dir", "", "Host directory where dated output artifacts are stored.")
	cronAddCmd.Flags().StringSliceVar(&cronSpecComponents, "component", nil, "Job component to include. Repeat to select multiple.")
	cronAddCmd.Flags().IntVar(&cronSpecRetentionDays, "retention-days", 14, "Delete non-monthly artifacts older than this many days.")
	cronAddCmd.Flags().BoolVar(&cronSpecPreserveFirstOfMonth, "preserve-first-of-month", true, "Keep artifacts created on day 01 of the month when pruning.")
	cronAddCmd.Flags().BoolVar(&cronSpecDockerPrune, "docker-prune", false, "Run docker system prune -af after a successful run.")

	cronCmd.AddCommand(cronAddCmd)
	cronCmd.AddCommand(cronListCmd)
	cronCmd.AddCommand(cronDeleteCmd)
	cronCmd.AddCommand(cronComponentsCmd)
	cronCmd.AddCommand(cronRunCmd)
	cronCmd.AddCommand(cronInstalledCmd)
	cronCmd.AddCommand(cronRenderSystemdCmd)
	cronCmd.GroupID = "ops"
	RootCmd.AddCommand(cronCmd)
}

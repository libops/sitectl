package cmd

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	tea "charm.land/bubbletea/v2"
	"github.com/libops/sitectl/pkg/config"
	corejob "github.com/libops/sitectl/pkg/job"
	"github.com/libops/sitectl/pkg/plugin"
	"github.com/spf13/cobra"
	"golang.org/x/term"
	yaml "gopkg.in/yaml.v3"
)

var jobCmd = &cobra.Command{
	Use:   "job",
	Short: "List and run plugin-defined jobs",
}

var jobListCmd = &cobra.Command{
	Use:   "list",
	Short: "List available jobs for the active or selected context",
	RunE: func(cmd *cobra.Command, args []string) error {
		contextName, err := config.ResolveCurrentContextName(cmd.Flags())
		if err != nil {
			return err
		}
		ctx, err := config.GetContext(contextName)
		if err != nil {
			return err
		}
		jobs, err := availableJobs(ctx)
		if err != nil {
			return err
		}
		if len(jobs) == 0 {
			fmt.Fprintf(cmd.OutOrStdout(), "No jobs available for context %q\n", ctx.Name)
			return nil
		}
		w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tPLUGIN\tDESCRIPTION")
		for _, spec := range jobs {
			fmt.Fprintf(w, "%s\t%s\t%s\n", spec.Name, spec.Plugin, spec.Description)
		}
		return w.Flush()
	},
}

var jobExecCmd = &cobra.Command{
	Use:                "run JOB [args...]",
	Short:              "Run a plugin-defined job",
	DisableFlagParsing: true,
	Args:               cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		filteredArgs, contextName, err := pluginContextArgs(args)
		if err != nil {
			return err
		}
		if len(filteredArgs) == 0 {
			return fmt.Errorf("job name is required")
		}
		jobName := filteredArgs[0]
		jobArgs := filteredArgs[1:]

		ctx, err := config.GetContext(contextName)
		if err != nil {
			return err
		}
		owner, name, err := resolveJobOwner(ctx, jobName)
		if err != nil {
			return err
		}
		if requestsHelp(jobArgs) {
			return renderJobHelp(cmd, ctx, owner, name, jobArgs)
		}
		output, err := runJobCommand(cmd, contextName, owner, name, jobArgs)
		if err != nil {
			return cleanPluginCommandError(err)
		}
		if strings.TrimSpace(output) != "" {
			_, _ = fmt.Fprint(cmd.OutOrStdout(), output)
		}
		return nil
	},
}

func availableJobs(ctx config.Context) ([]corejob.Spec, error) {
	owners := cronPluginsForContext(ctx.Plugin)
	var combined []corejob.Spec
	for _, owner := range owners {
		output, err := pluginSDK.InvokePluginCommand(owner, []string{"--context", ctx.Name, "__job", "list"}, plugin.CommandExecOptions{
			Context: RootCmd.Context(),
			Capture: true,
		})
		if err != nil {
			continue
		}
		var specs []corejob.Spec
		if err := yaml.Unmarshal([]byte(output), &specs); err != nil {
			return nil, fmt.Errorf("parse jobs from plugin %q: %w", owner, err)
		}
		for i := range specs {
			if strings.TrimSpace(specs[i].Plugin) == "" {
				specs[i].Plugin = owner
			}
		}
		combined = append(combined, specs...)
	}
	sort.Slice(combined, func(i, j int) bool {
		if combined[i].Plugin != combined[j].Plugin {
			return combined[i].Plugin < combined[j].Plugin
		}
		return combined[i].Name < combined[j].Name
	})
	return combined, nil
}

func resolveJobOwner(ctx config.Context, raw string) (string, string, error) {
	name := strings.TrimSpace(raw)
	if pluginName, jobName, ok := splitNamespacedComponent(name); ok {
		return pluginName, jobName, nil
	}
	jobs, err := availableJobs(ctx)
	if err != nil {
		return "", "", err
	}
	var matches []corejob.Spec
	for _, spec := range jobs {
		if strings.EqualFold(spec.Name, name) {
			matches = append(matches, spec)
		}
	}
	if len(matches) == 0 {
		return "", "", fmt.Errorf("job %q not found for context %q", name, ctx.Name)
	}
	if len(matches) > 1 {
		owners := make([]string, 0, len(matches))
		for _, spec := range matches {
			owners = append(owners, spec.Plugin)
		}
		sort.Strings(owners)
		return "", "", fmt.Errorf("job %q is ambiguous; qualify it as plugin/job (%s)", name, strings.Join(owners, ", "))
	}
	return matches[0].Plugin, matches[0].Name, nil
}

func pluginContextArgs(args []string) ([]string, string, error) {
	contextName, err := RootCmd.PersistentFlags().GetString("context")
	if err != nil {
		return nil, "", err
	}
	filtered := make([]string, 0, len(args))
	skipNext := false
	for _, arg := range args {
		if skipNext {
			contextName = arg
			skipNext = false
			continue
		}
		if arg == "--context" {
			skipNext = true
			continue
		}
		if strings.HasPrefix(arg, "--context=") {
			contextName = strings.TrimSpace(strings.TrimPrefix(arg, "--context="))
			continue
		}
		filtered = append(filtered, arg)
	}
	return filtered, contextName, nil
}

func init() {
	jobCmd.AddCommand(jobListCmd)
	jobCmd.AddCommand(jobExecCmd)
	RootCmd.AddCommand(jobCmd)
}

func runJobCommand(cmd *cobra.Command, contextName, owner, name string, jobArgs []string) (string, error) {
	invocation := append([]string{"--context", contextName, "__job", name}, jobArgs...)
	if stderrFile, ok := cmd.ErrOrStderr().(*os.File); ok && term.IsTerminal(int(stderrFile.Fd())) {
		return runJobWithProgress(cmd, owner, name, contextName, invocation)
	}
	return pluginSDK.InvokePluginCommand(owner, invocation, plugin.CommandExecOptions{
		Context: RootCmd.Context(),
		Capture: true,
	})
}

func runJobWithProgress(cmd *cobra.Command, owner, name, contextName string, invocation []string) (string, error) {
	m := newDebugSpinnerModel()
	m.title = "Running Job"
	m.detail = fmt.Sprintf("%s on %s", name, contextName)

	p := tea.NewProgram(m, tea.WithOutput(cmd.ErrOrStderr()))

	go func() {
		result, err := pluginSDK.InvokePluginCommand(owner, invocation, plugin.CommandExecOptions{
			Context: RootCmd.Context(),
			Capture: true,
		})
		p.Send(debugProgressDoneMsg{result: result, err: err})
	}()

	finalModel, err := p.Run()
	if err != nil {
		return "", fmt.Errorf("running job progress: %w", err)
	}
	done := finalModel.(debugSpinnerModel)
	return done.result, done.err
}

func requestsHelp(args []string) bool {
	for _, arg := range args {
		switch arg {
		case "--help", "-h":
			return true
		}
	}
	return false
}

func renderJobHelp(cmd *cobra.Command, ctx config.Context, owner, name string, jobArgs []string) error {
	output, err := pluginSDK.InvokePluginCommand(owner, append([]string{"--context", ctx.Name, "__job", name}, jobArgs...), plugin.CommandExecOptions{
		Context: RootCmd.Context(),
		Capture: true,
	})
	if err != nil {
		return cleanPluginCommandError(err)
	}

	replacements := []string{
		fmt.Sprintf("sitectl %s __job %s", owner, name), fmt.Sprintf("sitectl job run %s", name),
		fmt.Sprintf("%s __job %s", owner, name), fmt.Sprintf("job run %s", name),
		"Internal job execution command", "Run a plugin-defined job",
	}
	rewritten := strings.NewReplacer(replacements...).Replace(output)
	_, err = fmt.Fprint(cmd.OutOrStdout(), rewritten)
	return err
}

func cleanPluginCommandError(err error) error {
	message := err.Error()
	for _, prefix := range []string{
		`run plugin "drupal": exit status 1: `,
		`run plugin "isle": exit status 1: `,
		`run plugin "wordpress": exit status 1: `,
		`run plugin "core": exit status 1: `,
	} {
		if strings.HasPrefix(message, prefix) {
			return errors.New(strings.TrimPrefix(message, prefix))
		}
	}

	const genericPrefix = `run plugin "`
	if strings.HasPrefix(message, genericPrefix) {
		if idx := strings.Index(message, `: exit status 1: `); idx != -1 {
			return errors.New(message[idx+len(`: exit status 1: `):])
		}
	}
	return err
}
